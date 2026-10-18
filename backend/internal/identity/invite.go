package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Account invitations.
//
// # What this replaces
//
// Creating an officer used to mean typing a password for them and reading it
// out. The credential existed, in the clear, in whatever channel it was passed
// along, before its owner had touched it -- and it stayed valid until they got
// round to changing it. must_change_password narrowed what could be done with
// it; it did not stop anyone using it.
//
// An invitation is a single-use token that sets a password its author never
// sees, and expires whether or not it is used.
//
// # Why the token is hashed
//
// For the same reason the password is. Anyone who could read this table --
// a backup, a replica, a support query -- would otherwise hold a working
// credential for every account not yet claimed. The token is generated once,
// handed to the mailer, and never stored in a form that can be replayed.

// What a token is for. Stored, and part of the live-token index, so an
// invitation and a reset never revoke or block one another.
const (
	PurposeInvite = "invite"
	PurposeReset  = "reset"
)

// ResetTTL is how long a password reset link stays usable.
//
// Much shorter than an invitation. An invitation is sent to somebody who may
// be in a field for three days and does not yet have an account to protect; a
// reset is asked for by somebody sitting at a keyboard right now, and every
// extra hour it lives is an hour a link sitting in a mailbox is a way into a
// live account with real postings behind it.
const ResetTTL = 1 * time.Hour

// InviteTTL is how long an invitation stays usable.
//
// Long enough to survive a weekend and a district officer who checks email on
// Monday; short enough that an address that turns out to be wrong is not a
// standing credential.
const InviteTTL = 72 * time.Hour

// Errors a caller distinguishes. A token that has been used and a token that
// has expired are different things to say to the person holding it.
var (
	ErrInviteNotFound = errors.New("no such invitation")
	ErrInviteUsed     = errors.New("that invitation has already been used")
	ErrInviteExpired  = errors.New("that invitation has expired")
)

// Invite is a live invitation, as the accept screen needs it.
type Invite struct {
	ID        string
	UserID    string
	Email     string
	FullName  string
	Username  string
	ExpiresAt time.Time

	// Purpose is "invite" or "reset". The screen that consumes a token says
	// different things for each -- "set your password" against "choose a new
	// one" -- and, more importantly, a caller must never accept one where it
	// meant the other.
	Purpose string
}

// newToken returns a URL-safe token and its storage hash.
//
// 32 bytes from crypto/rand: this is a bearer credential for an officer
// account, and the only thing standing between a guess and a live posting.
func newToken() (token, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generating an invitation token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashToken(token), nil
}

// HashToken is the stored form of an invitation token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateInvite issues an invitation, revoking any live one for the account.
//
// Takes a Querier so it can run inside the transaction that created the
// account: an account that committed without its invitation is one nobody can
// claim, and an invitation that committed without its account is a token
// pointing at nothing.
func (s *Store) CreateInvite(
	ctx context.Context, q db.Querier, userID, email, createdBy string,
) (token string, expiresAt time.Time, err error) {
	return s.createToken(ctx, q, userID, email, createdBy, PurposeInvite, InviteTTL, "")
}

// CreateReset issues a password reset token.
//
// requestedIP is recorded because, unlike an invitation, nobody with a name
// asked for this -- whoever was at the keyboard did. Where the request came
// from is what an inquiry into a hijacked account has to start from.
func (s *Store) CreateReset(
	ctx context.Context, q db.Querier, userID, email, requestedIP string,
) (token string, expiresAt time.Time, err error) {
	return s.createToken(ctx, q, userID, email, "", PurposeReset, ResetTTL, requestedIP)
}

func (s *Store) createToken(
	ctx context.Context, q db.Querier, userID, email, createdBy, purpose string,
	ttl time.Duration, requestedIP string,
) (token string, expiresAt time.Time, err error) {
	// Issuing supersedes. A link forwarded to the wrong address stops working
	// the moment a fresh one is issued, which is the only remedy anyone has
	// when that happens -- and for a reset it is also what stops a stack of
	// live links accumulating from repeated requests.
	if _, err := q.Exec(ctx, `
		UPDATE account_invites SET revoked_at = now()
		 WHERE user_id = $1 AND purpose = $2
		   AND accepted_at IS NULL AND revoked_at IS NULL`,
		userID, purpose); err != nil {
		return "", time.Time{}, fmt.Errorf("superseding tokens: %w", err)
	}

	token, hash, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt = time.Now().Add(ttl)

	var by *string
	if createdBy != "" {
		by = &createdBy
	}
	var ip *string
	if requestedIP != "" {
		ip = &requestedIP
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO account_invites
		       (user_id, token_hash, email, expires_at, created_by, purpose, requested_ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		userID, hash, email, expiresAt, by, purpose, ip); err != nil {
		return "", time.Time{}, fmt.Errorf("recording the token: %w", err)
	}
	return token, expiresAt, nil
}

// InviteByToken resolves a token for the accept screen.
//
// Looked up by hash, so a token is never compared against anything stored in
// a replayable form.
func (s *Store) InviteByToken(ctx context.Context, token string) (*Invite, error) {
	var inv Invite
	var accepted, revoked *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT i.id, i.user_id, i.email, i.expires_at, i.accepted_at, i.revoked_at,
		       i.purpose, u.full_name, u.username
		  FROM account_invites i
		  JOIN users u ON u.id = i.user_id
		 WHERE i.token_hash = $1`, HashToken(token)).
		Scan(&inv.ID, &inv.UserID, &inv.Email, &inv.ExpiresAt, &accepted, &revoked,
			&inv.Purpose, &inv.FullName, &inv.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInviteNotFound
	}
	if err != nil {
		return nil, err
	}
	// A revoked invitation is reported as "not found" rather than "revoked".
	// It was superseded because somebody decided the holder should not have
	// it, and confirming that the token was once real tells them more than
	// they should learn from a public endpoint.
	if revoked != nil {
		return nil, ErrInviteNotFound
	}
	if accepted != nil {
		return nil, ErrInviteUsed
	}
	if time.Now().After(inv.ExpiresAt) {
		return nil, ErrInviteExpired
	}
	return &inv, nil
}

// AcceptInvite sets the account's password and consumes the invitation.
//
// The two happen in one transaction, and the invitation is consumed with the
// expiry and accepted checks in the UPDATE's own WHERE clause rather than in
// Go. Two requests arriving with the same token at the same moment both pass a
// check-then-act in the application; only one of them updates a row here.
func (s *Store) AcceptInvite(ctx context.Context, token, passwordHash string) (userID string, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	err = tx.QueryRow(ctx, `
		UPDATE account_invites
		   SET accepted_at = now()
		 WHERE token_hash = $1
		   AND accepted_at IS NULL
		   AND revoked_at IS NULL
		   AND expires_at > now()
		RETURNING user_id`, HashToken(token)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Nothing matched. Say which of the three it was, from a plain read,
		// so the person holding a stale link is told to ask for a new one
		// rather than that their token is malformed.
		return "", s.explainRejection(ctx, token)
	}
	if err != nil {
		return "", err
	}

	// must_change_password is false: this password was chosen by its owner
	// and nobody else has seen it, which is the whole point of the exchange.
	tag, err := tx.Exec(ctx, `
		UPDATE users
		   SET password_hash = $2, must_change_password = false,
		       password_changed_at = now(), status = 'active'
		 WHERE id = $1 AND kind = 'officer'`, userID, passwordHash)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return userID, tx.Commit(ctx)
}

// explainRejection turns a failed consume into the reason for it.
func (s *Store) explainRejection(ctx context.Context, token string) error {
	var accepted, revoked *time.Time
	var expires time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT accepted_at, revoked_at, expires_at
		  FROM account_invites WHERE token_hash = $1`, HashToken(token)).
		Scan(&accepted, &revoked, &expires)
	if errors.Is(err, pgx.ErrNoRows) || err != nil {
		return ErrInviteNotFound
	}
	switch {
	case revoked != nil:
		return ErrInviteNotFound
	case accepted != nil:
		return ErrInviteUsed
	case time.Now().After(expires):
		return ErrInviteExpired
	}
	return ErrInviteNotFound
}

// ByEmail finds the officer a reset was asked for.
//
// Returns ErrNotFound for an address nobody holds, and the caller must answer
// identically either way -- a reset form that says "no such account" is an
// account enumerator with a friendly face.
func (s *Store) OfficerByEmail(ctx context.Context, email string) (*User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users u
		  WHERE u.kind = 'officer' AND lower(u.email) = lower($1)`,
		strings.TrimSpace(email)))
	if err != nil {
		return nil, err
	}
	return s.withGrants(ctx, u)
}

// PendingInvite reports whether an account is still waiting to be claimed,
// so the console can show "invited" beside it rather than "active" -- an
// account nobody has signed into yet is not the same as one in use.
func (s *Store) PendingInvite(ctx context.Context, userID string) (*time.Time, error) {
	var expires time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT expires_at FROM account_invites
		 WHERE user_id = $1 AND accepted_at IS NULL AND revoked_at IS NULL`,
		userID).Scan(&expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &expires, nil
}

// ReinviteOfficer issues a fresh invitation for an account that has not yet
// been claimed, superseding any live one.
//
// Same transaction as CreateInvitedOfficer uses, and for the same reason: the
// old token must stop working and the new one must be queued together. Revoked
// without the replacement queued, an administrator has locked the officer out
// of an account nobody can now claim.
func (s *Store) ReinviteOfficer(
	ctx context.Context, user *User, createdBy string,
	queueEmail func(ctx context.Context, q db.Querier, token string, expires time.Time) error,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	token, expires, err := s.CreateInvite(ctx, tx, user.ID, user.Email, createdBy)
	if err != nil {
		return err
	}
	if queueEmail != nil {
		if err := queueEmail(ctx, tx, token, expires); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ResetOfficer issues a password reset for an account that already has one.
//
// Same transaction as an invitation, and for the same reason: the old token
// must stop working and the new one must be queued together, or a request
// leaves an account whose only live link has just been revoked.
func (s *Store) ResetOfficer(
	ctx context.Context, user *User, requestedIP string,
	queueEmail func(ctx context.Context, q db.Querier, token string, expires time.Time) error,
) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	token, expires, err := s.CreateReset(ctx, tx, user.ID, user.Email, requestedIP)
	if err != nil {
		return err
	}
	if queueEmail != nil {
		if err := queueEmail(ctx, tx, token, expires); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
