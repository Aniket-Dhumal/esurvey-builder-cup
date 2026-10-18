package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Service is the sign-in flow.
type Service struct {
	Users    *identity.Store
	Sessions *Sessions
	Throttle *Throttle
	OTP      *OTP
	Keys     *Keyring
	Audit    *audit.Recorder
	Log      *slog.Logger

	AccessTTL         time.Duration
	OfficerRefreshTTL time.Duration
	FarmerRefreshTTL  time.Duration

	// NameOfficerHandsets says whether the farmer sign-in may explain that a
	// number belongs to an officer.
	//
	// Off in a hardened environment, where the refusal stays uniform and an
	// officer is told what happened by a person rather than by a public
	// endpoint. On in development and in a trial, where the people typing
	// numbers are the team and the confusion is real: four attempts with no
	// explanation is how this was found.
	NameOfficerHandsets bool

	// FarmerAccessTTL is a farmer's access-token life. Zero means AccessTTL.
	//
	// Longer than an officer's, deliberately. Every expiry is a refresh, every
	// refresh a rotation, and every rotation a round trip on 2G and a record
	// in Redis. At fifteen minutes a farmer's phone rotated dozens of times a
	// day; that is what filled Redis and what turned lost replies into
	// sign-outs (docs/SYNC_SCALE_REVIEW.md, C3, C4). What a longer access
	// token costs is how long a revoked farmer session stays usable, and a
	// farmer's token reaches only that farmer's own claims.
	FarmerAccessTTL time.Duration
}

// accessTTLFor is the access-token life for this kind of account.
func (s *Service) accessTTLFor(kind rbac.Kind) time.Duration {
	if kind == rbac.KindFarmer && s.FarmerAccessTTL > 0 {
		return s.FarmerAccessTTL
	}
	return s.AccessTTL
}

// Tokens is what a successful sign-in returns.
type Tokens struct {
	TokenType        string    `json:"token_type"`
	AccessToken      string    `json:"access_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	ExpiresIn        int       `json:"expires_in"`
	RefreshToken     string    `json:"refresh_token"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at"`
}

// Caller describes where a request came from, for throttling and audit.
type Caller struct {
	IP        string
	UserAgent string
	RequestID string
}

// dummyHash is verified against when no account matches, so that a sign-in
// attempt for an unknown username costs the same time as one for a known
// username with the wrong password. Without it, response latency enumerates
// the officer roll.
var dummyHash string

func init() {
	h, err := HashPassword("timing-equalisation-placeholder")
	if err != nil {
		panic("auth: cannot hash placeholder: " + err.Error())
	}
	dummyHash = h
}

// LoginOfficer authenticates a username and password.
//
// Returns a Challenge instead of tokens where the account carries a mobile
// number: the password is only the first of two factors, and the second is a
// code to that handset. See officer_second_factor.go for why an account with
// no number still signs in on the password alone.
func (s *Service) LoginOfficer(
	ctx context.Context, username, password string, c Caller,
) (*Tokens, *identity.User, *Challenge, error) {
	if retryAfter, err := s.Throttle.Check(ctx, username, c.IP); err != nil {
		s.auditFailure(ctx, audit.ActionLoginLockout, username, c,
			map[string]any{"retry_after_seconds": int(retryAfter.Seconds())})
		return nil, nil, nil, fmt.Errorf("%w: try again in %s", ErrLockedOut,
			retryAfter.Round(time.Second))
	}

	user, err := s.Users.ByUsername(ctx, username)
	switch {
	case errors.Is(err, identity.ErrNotFound):
		// Verify against the placeholder anyway, then fail identically.
		_, _, _ = VerifyPassword(dummyHash, password)
		return nil, nil, nil, s.failLogin(ctx, username, c, "no such account")
	case err != nil:
		return nil, nil, nil, fmt.Errorf("looking up account: %w", err)
	case user.Kind != rbac.KindOfficer:
		// A farmer account has no password to check; saying so would confirm
		// the number belongs to someone.
		_, _, _ = VerifyPassword(dummyHash, password)
		return nil, nil, nil, s.failLogin(ctx, username, c, "not an officer account")
	case user.PasswordHash == "":
		// An invited account that nobody has claimed yet. Handled here rather
		// than falling through, because an empty hash is not a decodable one:
		// VerifyPassword would report it as corrupt, and every sign-in
		// attempt against a pending invitation would log an error about a
		// broken record when nothing is broken.
		//
		// Verified against the placeholder first so that the refusal takes
		// the same time as a wrong password. "This account exists but has not
		// been set up" is exactly what an attacker enumerating a district's
		// officers would like to learn.
		_, _, _ = VerifyPassword(dummyHash, password)
		return nil, nil, nil, s.failLogin(ctx, username, c, "account not yet claimed")
	}

	ok, needsRehash, err := VerifyPassword(user.PasswordHash, password)
	if err != nil {
		s.Log.Error("stored password hash is unreadable", "user_id", user.ID, "error", err)
		return nil, nil, nil, s.failLogin(ctx, username, c, "unreadable hash")
	}
	if !ok {
		return nil, nil, nil, s.failLogin(ctx, username, c, "wrong password")
	}

	if !user.Active() {
		// Checked after the password so that a suspended account is not
		// distinguishable from an active one by an attacker who lacks the
		// password.
		return nil, nil, nil, s.failLogin(ctx, username, c, "account "+string(user.Status))
	}

	// Cost parameters were raised since this hash was made; the plaintext is
	// in hand exactly once, so this is the only moment it can be upgraded.
	if needsRehash {
		if h, hErr := HashPassword(password); hErr == nil {
			if uErr := s.Users.SetPassword(ctx, user.ID, h, user.MustChangePassword); uErr != nil {
				s.Log.Warn("re-hashing password failed", "user_id", user.ID, "error", uErr)
			}
		}
	}

	_ = s.Throttle.Reset(ctx, username, c.IP)

	// The password is proved. Where a handset is on file, that is half of a
	// sign-in and nothing is issued until the other half arrives.
	if user.MobileE164 != "" {
		challenge, err := s.startSecondFactor(ctx, user, c)
		if err != nil {
			return nil, nil, nil, err
		}
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionOTPRequested, Outcome: audit.OutcomeAllow,
			ActorUserID: user.ID, ActorLabel: maskMobile(user.MobileE164),
			ActorIP: c.IP, RequestID: c.RequestID,
			Detail: map[string]any{
				"channel": s.OTP.Channel(),
				"purpose": "officer second factor",
			},
		})
		return nil, user, challenge, nil
	}

	// No number on file, so the password stands alone. Recorded, because the
	// answer to "which officers are not yet protected by a second factor" is
	// a list somebody should be working through.
	tokens, signedIn, err := s.issue(ctx, user, c, s.OfficerRefreshTTL,
		audit.ActionLoginSuccess)
	if err == nil {
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionLoginSuccess, Outcome: audit.OutcomeAllow,
			ActorUserID: user.ID, ActorLabel: displayName(user),
			ActorIP: c.IP, RequestID: c.RequestID,
			Detail: map[string]any{
				"second_factor": "none: no mobile number on this account",
			},
		})
	}
	return tokens, signedIn, nil, err
}

// RequestFarmerOTP sends a one-time code to a handset.
//
// # Why an unknown number gets one
//
// The account is not the authority here; the grant is. Signing in proves only
// that somebody holds a handset, and that alone reaches nothing: every record
// this app can open is opened by proving a Farmer ID and that farmer's date of
// birth, which mints a grant (see IssueFarmerGrant and the registry lookup).
//
// Requiring the account to exist first meant only a number AgriStack already
// held could sign in, and an officer had to enrol it. That number is, in the
// words of the lookup route, "routinely a number since lost, a relative's, or
// a shopkeeper's" -- so a farmer standing in a hailed field could not claim
// for it, which is the case this app exists for. The account is therefore
// created on the first correct code (see VerifyFarmerOTP), holding nothing
// but the handset it was proved from.
//
// It also removes what little this route used to leak: every number now gets
// the same answer and the same code, so nothing can be learned by asking.
//
// # Why an officer is refused
//
// This route authenticates with a code sent to a handset, which is the right
// trade for a farmer: they have no password and a shared handset is the norm.
// An officer's account is protected by a password and holds a district's
// worth of authority, and every account carries a mobile number. Without the
// kind check, that number is a second way into the account -- one that skips
// the password entirely, and in a development build puts the code in a log.
// LoginOfficer has refused the mirror case since it was written; this is the
// other half of the same rule.
func (s *Service) RequestFarmerOTP(ctx context.Context, rawMobile string, c Caller) error {
	mobile, err := NormalizeMobile(rawMobile)
	if err != nil {
		return err
	}

	if _, err := s.Throttle.Check(ctx, mobile, c.IP); err != nil {
		return err
	}

	// Refused identically whatever the reason: a suspended farmer and an
	// officer's handset both return success without a code. The reason is
	// recorded for the audit, never returned, because "that number belongs to
	// an officer" is precisely what an attacker enumerating a district would
	// like to learn.
	refuse := func(reason string) error {
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionOTPRequested, Outcome: audit.OutcomeDeny,
			ActorLabel: maskMobile(mobile), ActorIP: c.IP, RequestID: c.RequestID,
			Detail: map[string]any{"reason": reason},
		})
		// Counted against the address so that walking a range of numbers is
		// not free, and returns success either way.
		_, _ = s.Throttle.RecordFailure(ctx, mobile, c.IP)
		return nil
	}

	// A number with no account is a new handset, not a refusal.
	var userID string
	user, err := s.Users.ByMobile(ctx, mobile)
	switch {
	case errors.Is(err, identity.ErrNotFound):
	case err != nil:
		return fmt.Errorf("looking up handset: %w", err)
	case user.Kind != rbac.KindFarmer:
		if err := refuse("not a farmer account"); err != nil {
			return err
		}
		if s.NameOfficerHandsets {
			return ErrOfficerHandset
		}
		return nil
	case !user.Active():
		return refuse("no active account")
	default:
		userID = user.ID
	}

	if _, err := s.OTP.Request(ctx, mobile); err != nil {
		return err
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionOTPRequested, Outcome: audit.OutcomeAllow,
		ActorUserID: userID, ActorLabel: maskMobile(mobile),
		ActorIP: c.IP, RequestID: c.RequestID,
		Detail: map[string]any{
			"channel": s.OTP.Channel(),
			// Distinguishes a handset signing in for the first time from one
			// that already has an account, which is the difference between a
			// new user and a returning one in any later count.
			"new_handset": userID == "",
		},
	})
	return nil
}

// VerifyFarmerOTP exchanges a code for tokens.
func (s *Service) VerifyFarmerOTP(
	ctx context.Context, rawMobile, code string, c Caller,
) (*Tokens, *identity.User, error) {
	mobile, err := NormalizeMobile(rawMobile)
	if err != nil {
		return nil, nil, err
	}

	if retryAfter, err := s.Throttle.Check(ctx, mobile, c.IP); err != nil {
		return nil, nil, fmt.Errorf("%w: try again in %s", ErrLockedOut,
			retryAfter.Round(time.Second))
	}

	if err := s.OTP.Verify(ctx, mobile, code); err != nil {
		_, _ = s.Throttle.RecordFailure(ctx, mobile, c.IP)
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionOTPFailure, Outcome: audit.OutcomeDeny,
			ActorLabel: maskMobile(mobile), ActorIP: c.IP, RequestID: c.RequestID,
			Detail: map[string]any{"reason": err.Error()},
		})
		return nil, nil, err
	}

	user, err := s.Users.ByMobile(ctx, mobile)
	if errors.Is(err, identity.ErrNotFound) {
		// A handset signing in for the first time. The account is created
		// here rather than when the code was asked for, so that asking cannot
		// fill the table with accounts for numbers nobody holds.
		//
		// It carries the handset and nothing else: no farmer id, because which
		// farmers this account may act for is decided later and one at a time,
		// by proving a Farmer ID and date of birth. Until then the session
		// opens nothing -- rbac.Scope is empty, and every store refuses an
		// empty scope outright.
		//
		// The name is the masked number because nobody has proved one yet, and
		// the table requires something. It reads as what it is, cannot be
		// mistaken for a person, and is replaced by the registry's name the
		// moment an officer enrols this farmer (identity.NameFarmer).
		user, err = s.Users.CreateFarmer(ctx, identity.NewFarmer{
			FullName:   maskMobile(mobile),
			MobileE164: mobile,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("enrolling a new handset: %w", err)
		}
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionFarmerEnrolled, Outcome: audit.OutcomeAllow,
			ActorUserID: user.ID, ActorLabel: maskMobile(mobile),
			ActorIP: c.IP, RequestID: c.RequestID,
			ResourceType: "user", ResourceID: user.ID,
			Detail: map[string]any{"how": "one-time code to a new handset"},
		})
	} else if err != nil {
		return nil, nil, fmt.Errorf("looking up handset: %w", err)
	}
	if user.Kind != rbac.KindFarmer {
		// Checked again rather than trusted from the request: a code held
		// from before the account changed hands must not become a session,
		// and a route that mints sessions should not depend on another
		// function having refused earlier. Reported as a bad code, because
		// the caller is not owed the distinction.
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionOTPFailure, Outcome: audit.OutcomeDeny,
			ActorUserID: user.ID, ActorLabel: maskMobile(mobile),
			ActorIP: c.IP, RequestID: c.RequestID,
			Detail: map[string]any{"reason": "not a farmer account"},
		})
		_, _ = s.Throttle.RecordFailure(ctx, mobile, c.IP)
		return nil, nil, ErrOTPIncorrect
	}
	if !user.Active() {
		return nil, nil, identity.ErrInactive
	}

	_ = s.Throttle.Reset(ctx, mobile, c.IP)
	return s.issue(ctx, user, c, s.FarmerRefreshTTL, audit.ActionLoginSuccess)
}

// Refresh exchanges a refresh token for a new pair.
//
// Grants are re-read from the database on every refresh. That is what bounds
// the staleness of an access token: a posting revoked now stops working
// within one access lifetime rather than whenever the session happens to end.
func (s *Service) Refresh(ctx context.Context, refreshToken string, c Caller) (*Tokens, error) {
	issued, prior, err := s.Sessions.Rotate(ctx, refreshToken, c.IP, c.UserAgent, s.OfficerRefreshTTL)
	if errors.Is(err, ErrRefreshReused) {
		// The family is already revoked by the time this returns. Recorded
		// synchronously: a stolen refresh token is exactly the entry an
		// investigation comes looking for.
		_ = s.Audit.RecordSync(ctx, audit.Event{
			Action: audit.ActionRefreshReuse, Outcome: audit.OutcomeDeny,
			ActorIP: c.IP, RequestID: c.RequestID,
			Detail: map[string]any{
				"note": "presented token had already been used; the whole session family was revoked",
			},
		})
		return nil, err
	}
	if err != nil {
		return nil, err
	}

	user, err := s.Users.ByID(ctx, prior.UserID)
	if err != nil {
		return nil, fmt.Errorf("reloading account: %w", err)
	}
	if !user.Active() {
		_ = s.Sessions.RevokeAllForUser(ctx, user.ID)
		return nil, identity.ErrInactive
	}

	// A farmer's session may outlive an officer's; the rotation above used
	// the officer TTL, so correct it now that the account kind is known.
	if user.Kind == rbac.KindFarmer && s.FarmerRefreshTTL > s.OfficerRefreshTTL {
		if err := s.Sessions.Extend(ctx, issued.Token, s.FarmerRefreshTTL); err != nil {
			s.Log.Warn("extending farmer session", "error", err)
		}
		issued.ExpiresAt = time.Now().UTC().Add(s.FarmerRefreshTTL)
	}

	principal := user.Principal(issued.SessionID)
	accessTTL := s.accessTTLFor(user.Kind)
	access, exp, err := s.Keys.Issue(principal, accessTTL)
	if err != nil {
		return nil, err
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionRefresh, Outcome: audit.OutcomeAllow,
		ActorUserID: user.ID, ActorRole: string(principal.PrimaryRole()),
		ActorIP: c.IP, RequestID: c.RequestID,
	})

	return &Tokens{
		TokenType:        "Bearer",
		AccessToken:      access,
		ExpiresAt:        exp,
		ExpiresIn:        int(accessTTL.Seconds()),
		RefreshToken:     issued.Token,
		RefreshExpiresAt: issued.ExpiresAt,
	}, nil
}

// Logout ends the session the refresh token belongs to.
func (s *Service) Logout(ctx context.Context, refreshToken string, c Caller) error {
	if err := s.Sessions.Revoke(ctx, refreshToken); err != nil {
		return err
	}
	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionLogout, Outcome: audit.OutcomeAllow,
		ActorIP: c.IP, RequestID: c.RequestID,
	}.FromContext(ctx))
	return nil
}

// ChangePassword sets a new password for an officer.
//
// Every session ends, including the caller's own. Signing back in is a small
// cost, and the alternative -- leaving other sessions alive after a password
// change -- is exactly wrong when the reason for the change is that someone
// else knew the old one.
func (s *Service) ChangePassword(
	ctx context.Context, userID, current, next string, c Caller,
) error {
	user, err := s.Users.ByID(ctx, userID)
	if err != nil {
		return err
	}
	if user.Kind != rbac.KindOfficer {
		return errors.New("only officer accounts have passwords")
	}

	ok, _, err := VerifyPassword(user.PasswordHash, current)
	if err != nil || !ok {
		_, _ = s.Throttle.RecordFailure(ctx, user.Username, c.IP)
		return ErrInvalidCredentials
	}
	if current == next {
		return fmt.Errorf("%w: the new password matches the old one", ErrWeakPassword)
	}
	if err := CheckPasswordPolicy(next, user.Username, user.FullName); err != nil {
		return err
	}

	hash, err := HashPassword(next)
	if err != nil {
		return err
	}
	if err := s.Users.SetPassword(ctx, user.ID, hash, false); err != nil {
		return err
	}
	if err := s.Sessions.RevokeAllForUser(ctx, user.ID); err != nil {
		s.Log.Warn("revoking sessions after password change", "user_id", user.ID, "error", err)
	}

	return s.Audit.RecordSync(ctx, audit.Event{
		Action: audit.ActionPasswordChanged, Outcome: audit.OutcomeAllow,
		ActorUserID: user.ID, ActorLabel: user.Username,
		ActorIP: c.IP, RequestID: c.RequestID,
	})
}

func (s *Service) issue(
	ctx context.Context, user *identity.User, c Caller,
	refreshTTL time.Duration, action string,
) (*Tokens, *identity.User, error) {
	issued, err := s.Sessions.Start(ctx, user.ID, c.IP, c.UserAgent, refreshTTL)
	if err != nil {
		return nil, nil, err
	}

	principal := user.Principal(issued.SessionID)
	accessTTL := s.accessTTLFor(user.Kind)
	access, exp, err := s.Keys.Issue(principal, accessTTL)
	if err != nil {
		return nil, nil, err
	}

	if err := s.Users.TouchLastLogin(ctx, user.ID); err != nil {
		s.Log.Warn("recording last login", "user_id", user.ID, "error", err)
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: action, Outcome: audit.OutcomeAllow,
		ActorUserID: user.ID, ActorLabel: displayName(user),
		ActorRole: string(principal.PrimaryRole()),
		ActorIP:   c.IP, RequestID: c.RequestID,
		Detail: map[string]any{
			"kind":       string(user.Kind),
			"session_id": issued.SessionID,
		},
	})

	return &Tokens{
		TokenType:        "Bearer",
		AccessToken:      access,
		ExpiresAt:        exp,
		ExpiresIn:        int(accessTTL.Seconds()),
		RefreshToken:     issued.Token,
		RefreshExpiresAt: issued.ExpiresAt,
	}, user, nil
}

// failLogin records the attempt and returns the one error every failure path
// shares. The reason is written to the audit log, never to the caller.
func (s *Service) failLogin(ctx context.Context, username string, c Caller, reason string) error {
	locked, err := s.Throttle.RecordFailure(ctx, username, c.IP)
	if err != nil {
		s.Log.Warn("recording failed sign-in", "error", err)
	}
	s.auditFailure(ctx, audit.ActionLoginFailure, username, c,
		map[string]any{"reason": reason, "locked": locked > 0})
	return ErrInvalidCredentials
}

func (s *Service) auditFailure(
	ctx context.Context, action, label string, c Caller, detail map[string]any,
) {
	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: action, Outcome: audit.OutcomeDeny,
		ActorLabel: label, ActorIP: c.IP, RequestID: c.RequestID,
		Detail: detail,
	})
}

func displayName(u *identity.User) string {
	if u.Username != "" {
		return u.Username
	}
	return maskMobile(u.MobileE164)
}

// IssueFarmerGrant records that an account has answered the knowledge check
// for one registry identity. See farmer_grant.go for what a grant is and is
// not.
func (s *Service) IssueFarmerGrant(userID, farmerID string, ttl time.Duration) (string, time.Time, error) {
	return s.Keys.IssueFarmerGrant(userID, farmerID, ttl)
}

// FarmerGranted returns the farmer a grant names, if it is valid and was
// issued to this account.
func (s *Service) FarmerGranted(token, userID string) (string, error) {
	return s.Keys.FarmerGranted(token, userID)
}
