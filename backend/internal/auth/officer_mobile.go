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
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Putting a handset on an officer's own account, and proving it is theirs.
//
// The number becomes the second factor for this account (see
// officer_second_factor.go), so it is saved only after a code sent to it comes
// back. An unverified number is worse than none: sign-in would then demand a
// code that can never arrive, and a typo would lock an officer out of a live
// system.

// ErrNotAnOfficer is a farmer using an officer-only route. A farmer's handset
// comes from the registry and changing it here would let a session move
// somebody else's claims to a new phone.
var ErrNotAnOfficer = errors.New("this is not an officer account")

// mobileChangePurpose keeps these codes apart from a sign-in's.
const mobileChangePurpose = "mobile"

const keyPrefixMobileChange = "mobchg:"

type mobileChangeRecord struct {
	UserID string `json:"u"`
	Mobile string `json:"m"`
}

// StartMobileChange sends a code to the number an officer wants to use.
func (s *Service) StartMobileChange(
	ctx context.Context, userID, rawMobile string, c Caller,
) (*Challenge, error) {
	user, err := s.Users.ByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("reading the account: %w", err)
	}
	if user.Kind != rbac.KindOfficer {
		return nil, ErrNotAnOfficer
	}

	mobile, err := NormalizeMobile(rawMobile)
	if err != nil {
		return nil, err
	}

	// Refused before a code is sent rather than after: the officer should
	// learn the number is taken while they are still looking at the field
	// they typed it in, not a minute later holding a code.
	if other, err := s.Users.ByMobile(ctx, mobile); err == nil && other.ID != userID {
		return nil, identity.ErrConflict
	} else if err != nil && !errors.Is(err, identity.ErrNotFound) {
		return nil, fmt.Errorf("checking the number: %w", err)
	}

	if _, err := s.OTP.For(mobileChangePurpose, 0).Request(ctx, mobile); err != nil {
		return nil, err
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generating a challenge: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	body, err := json.Marshal(mobileChangeRecord{UserID: userID, Mobile: mobile})
	if err != nil {
		return nil, err
	}
	if err := s.Sessions.rdb.Set(ctx,
		keyPrefixMobileChange+hashToken(token), body, challengeTTL).Err(); err != nil {
		return nil, fmt.Errorf("storing the challenge: %w", err)
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionOTPRequested, Outcome: audit.OutcomeAllow,
		ActorUserID: userID, ActorLabel: maskMobile(mobile),
		ActorIP: c.IP, RequestID: c.RequestID,
		Detail: map[string]any{"purpose": "verifying a new handset"},
	})

	return &Challenge{
		Token:      token,
		MobileHint: maskMobile(mobile),
		ExpiresAt:  time.Now().UTC().Add(challengeTTL),
	}, nil
}

// ConfirmMobileChange saves the number once its code comes back.
func (s *Service) ConfirmMobileChange(
	ctx context.Context, userID, token, code string, c Caller,
) (*identity.User, error) {
	if token == "" || code == "" {
		return nil, ErrChallengeInvalid
	}
	key := keyPrefixMobileChange + hashToken(token)

	raw, err := s.Sessions.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrChallengeInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("reading the challenge: %w", err)
	}
	var rec mobileChangeRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return nil, fmt.Errorf("decoding the challenge: %w", err)
	}
	if rec.UserID != userID {
		// A challenge belongs to the session that started it. Without this,
		// one officer's verified number could be attached to another account.
		return nil, ErrChallengeInvalid
	}

	if err := s.OTP.For(mobileChangePurpose, 0).Verify(ctx, rec.Mobile, code); err != nil {
		return nil, err
	}
	s.Sessions.rdb.Del(ctx, key)

	user, err := s.Users.SetMobile(ctx, userID, rec.Mobile)
	if err != nil {
		return nil, err
	}
	return user, nil
}

// RemoveMobile takes the handset off an officer's account.
//
// Allowed, because a lost phone must not be a locked account, and the
// alternative is an officer telephoning an administrator to be let back in.
// It is recorded: the account drops to a password alone, which is a fact
// somebody auditing second-factor coverage needs to see.
func (s *Service) RemoveMobile(
	ctx context.Context, userID string, c Caller,
) (*identity.User, error) {
	user, err := s.Users.ByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("reading the account: %w", err)
	}
	if user.Kind != rbac.KindOfficer {
		return nil, ErrNotAnOfficer
	}
	return s.Users.SetMobile(ctx, userID, "")
}
