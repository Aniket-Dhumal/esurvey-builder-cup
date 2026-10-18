package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
)

// The second half of an officer's sign-in.
//
// # Why a code is added rather than offered
//
// An officer signs in with a password and holds a district's worth of
// authority: their session approves claims worth money. A one-time code to
// their handset *instead of* the password would make the phone the credential,
// and a phone is recoverable by anyone who can persuade a shop to issue a new
// SIM. As well as the password it is the opposite -- an attacker now needs the
// password and the handset.
//
// So this is never an alternative route in. LoginOfficer still verifies the
// password first and issues nothing until it passes; the code is asked for
// afterwards, and only a correct one produces tokens.
//
// # Why an officer with no number still signs in
//
// Three of the eight officer accounts on this deployment have a mobile number;
// the rest were created by an administrator who was never asked for one.
// Refusing those accounts would lock five officers out of a live system to
// enforce a factor they were never given, so an account with no number signs
// in on its password alone and is recorded as having done so. What fixes that
// is collecting numbers, not a stricter check here.
//
// # What the challenge is
//
// A random token standing for "this password was accepted, moments ago, for
// this account". It is stored hashed, expires with the code, is single use,
// and carries its own attempt count so that guessing at the code is bounded
// even though the password is already spent.

// ErrChallengeInvalid is a second-factor challenge that is unknown, expired,
// or already spent.
var ErrChallengeInvalid = errors.New("that sign-in has expired; start again")

// OfficerCodePurpose separates these codes from a farmer's in Redis.
const OfficerCodePurpose = "officer"

// OfficerCodeDailyCap is how many codes one officer's handset may carry in a
// day. Higher than a farmer's ten: an officer signs in every morning, and on
// a bad day several times.
const OfficerCodeDailyCap = 40

// challengeTTL is how long the second step stays open. Short, because the
// password is already proved by this point and a long window is a window.
const challengeTTL = 5 * time.Minute

// maxChallengeAttempts bounds guessing at the code within one challenge.
const maxChallengeAttempts = 5

const keyPrefixChallenge = "2fa:"

// Challenge is what the first step returns when a second factor is required.
type Challenge struct {
	// Token is presented to VerifyOfficerCode. Not stored anywhere but the
	// client: Redis holds its hash.
	Token string `json:"challenge"`

	// MobileHint is the masked number the code went to, so an officer with
	// two handsets knows which one to pick up.
	MobileHint string `json:"mobile_hint"`

	ExpiresAt time.Time `json:"expires_at"`
}

type challengeRecord struct {
	UserID   string `json:"u"`
	Mobile   string `json:"m"`
	Attempts int    `json:"a"`
}

// startSecondFactor issues a code and the challenge that stands for it.
func (s *Service) startSecondFactor(
	ctx context.Context, user *identity.User, c Caller,
) (*Challenge, error) {
	if _, err := s.OfficerOTP().Request(ctx, user.MobileE164); err != nil {
		return nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generating a challenge: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	body, err := json.Marshal(challengeRecord{UserID: user.ID, Mobile: user.MobileE164})
	if err != nil {
		return nil, err
	}
	if err := s.Sessions.rdb.Set(ctx,
		keyPrefixChallenge+hashToken(token), body, challengeTTL).Err(); err != nil {
		return nil, fmt.Errorf("storing the challenge: %w", err)
	}

	return &Challenge{
		Token:      token,
		MobileHint: maskMobile(user.MobileE164),
		ExpiresAt:  time.Now().UTC().Add(challengeTTL),
	}, nil
}

// OfficerOTP is the code service for second factors.
func (s *Service) OfficerOTP() *OTP {
	return s.OTP.For(OfficerCodePurpose, OfficerCodeDailyCap)
}

// VerifyOfficerCode completes a sign-in that asked for a second factor.
func (s *Service) VerifyOfficerCode(
	ctx context.Context, token, code string, c Caller,
) (*Tokens, *identity.User, error) {
	if token == "" || code == "" {
		return nil, nil, ErrChallengeInvalid
	}
	key := keyPrefixChallenge + hashToken(token)

	raw, err := s.Sessions.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil, ErrChallengeInvalid
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the challenge: %w", err)
	}
	var rec challengeRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, nil, fmt.Errorf("decoding the challenge: %w", err)
	}

	if err := s.OfficerOTP().Verify(ctx, rec.Mobile, code); err != nil {
		rec.Attempts++
		if rec.Attempts >= maxChallengeAttempts {
			// The password is already spent; a fresh sign-in is cheap for the
			// officer and expensive for somebody guessing at six digits.
			s.Sessions.rdb.Del(ctx, key)
		} else if body, mErr := json.Marshal(rec); mErr == nil {
			// The original expiry is kept: a wrong guess must not extend the
			// life of what it is guessing at.
			s.Sessions.rdb.Set(ctx, key, body, redisTTL(ctx, s.Sessions.rdb, key))
		}
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionOTPFailure, Outcome: audit.OutcomeDeny,
			ActorUserID: rec.UserID, ActorLabel: maskMobile(rec.Mobile),
			ActorIP: c.IP, RequestID: c.RequestID,
			Detail: map[string]any{"reason": "second factor", "attempts": rec.Attempts},
		})
		return nil, nil, err
	}

	// Single use, whatever happens next.
	s.Sessions.rdb.Del(ctx, key)

	user, err := s.Users.ByID(ctx, rec.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("reloading the account: %w", err)
	}
	if !user.Active() {
		// Checked again: an account suspended between the two steps must not
		// complete a sign-in begun before it was.
		return nil, nil, identity.ErrInactive
	}

	_ = s.Throttle.Reset(ctx, user.Username, c.IP)
	return s.issue(ctx, user, c, s.OfficerRefreshTTL, audit.ActionLoginSuccess)
}

// redisTTL is what is left of a key's life, for rewriting it without
// extending it.
func redisTTL(ctx context.Context, rdb *redis.Client, key string) time.Duration {
	ttl, err := rdb.TTL(ctx, key).Result()
	if err != nil || ttl <= 0 {
		return challengeTTL
	}
	return ttl
}
