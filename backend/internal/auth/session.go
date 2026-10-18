package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Refresh tokens.
//
// An access token cannot be withdrawn once issued -- that is the price of
// checking it without a database round trip. The refresh token is where
// revocation actually lives, so it is opaque, stored server-side, single-use,
// and organised into families.
//
// # Rotation and reuse detection
//
// Each refresh consumes the presented token and issues a new one in the same
// family. A token that is presented twice therefore means one of two things:
// a client retried after a dropped response, or a stolen token is being used
// alongside the legitimate one. There is no way to tell them apart, so the
// whole family is revoked and the user signs in again. That is the behaviour
// OAuth's security BCP settles on, and the reason is worth stating: the cost
// of the false positive is one sign-in, and the cost of the false negative is
// an attacker with indefinite access.
//
// Only the SHA-256 of a token is stored. A dump of Redis is then not a set of
// usable credentials.
//
// # The one reuse that is not treated as theft
//
// Revoking on every reuse signed farmers out in two ordinary situations,
// measured in the field conditions this app runs in (docs/SYNC_SCALE_REVIEW.md,
// C4):
//
//  1. A reply lost on 2G. The server rotated; the phone never heard, and the
//     only token it has is the one just spent.
//  2. Two processes on one phone. The background worker rotates; the app,
//     holding the previous token, refreshes next.
//
// At lakhs of farmers that was thousands of forced OTP sign-ins a day, each
// needing signal and an SMS.
//
// Both look the same from here: the token presented is the one immediately
// before a successor that has never been used. So that case, and only that
// case, is answered with a fresh successor instead of revocation -- and the
// unused successor is destroyed, so there is still only one live token in the
// family. It is honoured for ReuseGrace after the rotation.
//
// Anything else is still theft: a token older than the previous one, or a
// previous token whose successor has already been presented. A thief holding a
// copy of the current token can race the farmer only until the farmer's own
// app next refreshes, at which point one of them presents a token whose
// successor is in use, and the family goes.

const (
	refreshTokenBytes = 32

	keyPrefixToken  = "rt:"    // rt:<sha256>      -> refreshRecord
	keyPrefixUsed   = "rtu:"   // rtu:<sha256>     -> family id
	keyPrefixFamily = "rtf:"   // rtf:<family>     -> set of token hashes
	keyPrefixUser   = "usess:" // usess:<user id> -> set of family ids
)

// ReuseGrace is how long after a rotation the previous token may be presented
// again, provided its successor was never used.
//
// Three days: a reply lost as a farmer walks out of signal is retried when they
// next have some, which in a low-coverage village can be the next market day.
const ReuseGrace = 72 * time.Hour

// tombstoneTTL is how long a spent token is remembered.
//
// It used to be the session's own life plus a day -- thirty-one days for a
// farmer, for every rotation. At 137 bytes each that filled a 256 MB Redis at
// around sixty thousand farmers, and the eviction policy then removed one-time
// codes first (docs/SYNC_SCALE_REVIEW.md, C3). Remembering a spent token past
// the grace window buys only the difference between "refused as reused" and
// "refused as unknown"; both are refusals.
const tombstoneTTL = ReuseGrace

// Session errors.
var (
	ErrRefreshUnknown = errors.New("refresh token is not recognised")
	ErrRefreshReused  = errors.New("refresh token has already been used")
)

// refreshRecord is what a live refresh token points at.
type refreshRecord struct {
	UserID    string    `json:"uid"`
	SessionID string    `json:"sid"`
	FamilyID  string    `json:"fid"`
	IssuedAt  time.Time `json:"iat"`
	ExpiresAt time.Time `json:"exp"`
	IP        string    `json:"ip,omitempty"`
	UserAgent string    `json:"ua,omitempty"`
}

// Sessions manages refresh tokens in Redis.
type Sessions struct {
	rdb *redis.Client
}

// NewSessions builds a session store.
func NewSessions(rdb *redis.Client) *Sessions { return &Sessions{rdb: rdb} }

// Issued is a newly minted refresh token.
type Issued struct {
	Token     string
	SessionID string
	FamilyID  string
	ExpiresAt time.Time
}

// Start opens a new session family. Used at sign-in.
func (s *Sessions) Start(
	ctx context.Context, userID, ip, userAgent string, ttl time.Duration,
) (*Issued, error) {
	family, err := randomID()
	if err != nil {
		return nil, err
	}
	session, err := randomID()
	if err != nil {
		return nil, err
	}
	return s.mint(ctx, userID, session, family, ip, userAgent, ttl)
}

// Rotate consumes a refresh token and issues its successor.
//
// On reuse it revokes the entire family and returns ErrRefreshReused, which
// the caller must treat as a security event rather than a failed request --
// except for the one reuse described at the top of this file.
//
// Atomic: the read and the writes run under WATCH, so two refreshes presenting
// the same token at the same instant cannot both mint. The loser retries and
// finds the tombstone the winner left.
func (s *Sessions) Rotate(
	ctx context.Context, presented, ip, userAgent string, ttl time.Duration,
) (*Issued, *refreshRecord, error) {
	hash := hashToken(presented)

	// Contention is only ever the same token presented at the same instant --
	// two processes on one phone, or a retry racing its own original -- so a
	// few attempts with a short random pause settle it.
	for attempt := 0; attempt < rotateAttempts; attempt++ {
		issued, rec, familyToRevoke, err := s.rotateOnce(ctx, hash, ip, userAgent, ttl)
		if errors.Is(err, redis.TxFailedErr) {
			if werr := sleepJitter(ctx, attempt); werr != nil {
				return nil, nil, werr
			}
			continue
		}
		if familyToRevoke != "" {
			if rerr := s.revokeFamily(ctx, familyToRevoke); rerr != nil {
				return nil, nil, fmt.Errorf("revoking reused family: %w", rerr)
			}
			return nil, nil, ErrRefreshReused
		}
		return issued, rec, err
	}
	return nil, nil, fmt.Errorf("rotating refresh token: %w", redis.TxFailedErr)
}

const rotateAttempts = 20

// sleepJitter waits a random 1..(attempt+1)*5 ms.
func sleepJitter(ctx context.Context, attempt int) error {
	var b [1]byte
	_, _ = rand.Read(b[:])
	d := time.Duration(1+int(b[0])%((attempt+1)*5)) * time.Millisecond
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// rotateOnce is one optimistic attempt. It returns a family id when the
// presented token is a reuse that must revoke it.
func (s *Sessions) rotateOnce(
	ctx context.Context, hash, ip, userAgent string, ttl time.Duration,
) (issued *Issued, prior *refreshRecord, revoke string, err error) {
	liveKey, usedKey := keyPrefixToken+hash, keyPrefixUsed+hash

	err = s.rdb.Watch(ctx, func(tx *redis.Tx) error {
		raw, gerr := tx.Get(ctx, liveKey).Result()
		switch {
		case gerr == nil:
			var rec refreshRecord
			if jerr := json.Unmarshal([]byte(raw), &rec); jerr != nil {
				return fmt.Errorf("decoding refresh record: %w", jerr)
			}
			next, err := newRefresh(rec, ip, userAgent, ttl)
			if err != nil {
				return err
			}
			_, perr := tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				// Burned and tombstoned in the same transaction that mints the
				// successor: there is never a moment with two live tokens, nor
				// one with none.
				p.Del(ctx, liveKey)
				p.SRem(ctx, keyPrefixFamily+rec.FamilyID, hash)
				p.Set(ctx, usedKey, tombstone{
					family: rec.FamilyID, next: next.hash, at: time.Now().UTC(),
				}.String(), tombstoneTTL)
				writeRefresh(ctx, p, next, ttl)
				return nil
			})
			if perr != nil {
				return perr
			}
			issued, prior = next.issued(), &rec
			return nil

		case !errors.Is(gerr, redis.Nil):
			return fmt.Errorf("reading refresh token: %w", gerr)
		}

		// Not live. Is it a token we spent?
		tomb, terr := tx.Get(ctx, usedKey).Result()
		if errors.Is(terr, redis.Nil) {
			return ErrRefreshUnknown
		}
		if terr != nil {
			return fmt.Errorf("reading spent token: %w", terr)
		}
		t := parseTombstone(tomb)

		// The forgiven case: the immediately previous token, within the grace
		// window, whose successor nobody has presented.
		if t.next != "" && time.Since(t.at) < ReuseGrace {
			successorKey := keyPrefixToken + t.next
			if werr := tx.Watch(ctx, successorKey).Err(); werr != nil {
				return werr
			}
			sraw, serr := tx.Get(ctx, successorKey).Result()
			if serr == nil {
				var rec refreshRecord
				if jerr := json.Unmarshal([]byte(sraw), &rec); jerr != nil {
					return fmt.Errorf("decoding refresh record: %w", jerr)
				}
				next, err := newRefresh(rec, ip, userAgent, ttl)
				if err != nil {
					return err
				}
				_, perr := tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
					// The successor that never arrived is destroyed, not left
					// alongside the new one.
					p.Del(ctx, successorKey)
					p.SRem(ctx, keyPrefixFamily+rec.FamilyID, t.next)
					p.Set(ctx, keyPrefixUsed+t.next, tombstone{
						family: rec.FamilyID, next: next.hash, at: time.Now().UTC(),
					}.String(), tombstoneTTL)
					// The grace is measured from the first rotation, so it
					// cannot be renewed indefinitely by presenting the same
					// old token again and again.
					p.Set(ctx, usedKey, tombstone{
						family: rec.FamilyID, next: next.hash, at: t.at,
					}.String(), max(time.Until(t.at.Add(ReuseGrace)), time.Second))
					writeRefresh(ctx, p, next, ttl)
					return nil
				})
				if perr != nil {
					return perr
				}
				issued, prior = next.issued(), &rec
				return nil
			}
			if !errors.Is(serr, redis.Nil) {
				return fmt.Errorf("reading successor token: %w", serr)
			}
		}

		// A verdict of theft is committed like any write, so it is only
		// reached from a consistent view: if another refresh changed these
		// keys since they were read -- a concurrent retry of the same lost
		// reply, say -- EXEC fails and the attempt starts again, rather than
		// revoking a family on the strength of a read that is already stale.
		if _, perr := tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.Exists(ctx, usedKey)
			return nil
		}); perr != nil {
			return perr
		}
		revoke = t.family
		return nil
	}, liveKey, usedKey)

	return issued, prior, revoke, err
}

// tombstone is what a spent token leaves behind: its family, the token that
// replaced it, and when.
type tombstone struct {
	family string
	next   string
	at     time.Time
}

// String is compact on purpose -- there is one per rotation.
func (t tombstone) String() string {
	return t.family + "|" + t.next + "|" + strconv.FormatInt(t.at.Unix(), 10)
}

// parseTombstone reads either form. A tombstone written before successors
// were recorded is a bare family id, and is always treated as a reuse.
func parseTombstone(v string) tombstone {
	parts := strings.Split(v, "|")
	if len(parts) != 3 {
		return tombstone{family: v}
	}
	secs, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return tombstone{family: parts[0]}
	}
	return tombstone{family: parts[0], next: parts[1], at: time.Unix(secs, 0).UTC()}
}

// pendingRefresh is a successor built but not yet written.
type pendingRefresh struct {
	token string
	hash  string
	rec   refreshRecord
}

func (p pendingRefresh) issued() *Issued {
	return &Issued{
		Token:     p.token,
		SessionID: p.rec.SessionID,
		FamilyID:  p.rec.FamilyID,
		ExpiresAt: p.rec.ExpiresAt,
	}
}

func newRefresh(from refreshRecord, ip, userAgent string, ttl time.Duration) (pendingRefresh, error) {
	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return pendingRefresh{}, fmt.Errorf("generating refresh token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	return pendingRefresh{
		token: token,
		hash:  hashToken(token),
		rec: refreshRecord{
			UserID:    from.UserID,
			SessionID: from.SessionID,
			FamilyID:  from.FamilyID,
			IssuedAt:  now,
			ExpiresAt: now.Add(ttl),
			IP:        ip,
			UserAgent: truncate(userAgent, 200),
		},
	}, nil
}

func writeRefresh(ctx context.Context, p redis.Pipeliner, r pendingRefresh, ttl time.Duration) {
	body, _ := json.Marshal(r.rec)
	p.Set(ctx, keyPrefixToken+r.hash, body, ttl)
	p.SAdd(ctx, keyPrefixFamily+r.rec.FamilyID, r.hash)
	p.Expire(ctx, keyPrefixFamily+r.rec.FamilyID, ttl+24*time.Hour)
	p.SAdd(ctx, keyPrefixUser+r.rec.UserID, r.rec.FamilyID)
	p.Expire(ctx, keyPrefixUser+r.rec.UserID, ttl+24*time.Hour)
}

// mint writes a new token into an existing or new family.
func (s *Sessions) mint(
	ctx context.Context, userID, sessionID, familyID, ip, userAgent string,
	ttl time.Duration,
) (*Issued, error) {
	raw := make([]byte, refreshTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	hash := hashToken(token)

	now := time.Now().UTC()
	rec := refreshRecord{
		UserID:    userID,
		SessionID: sessionID,
		FamilyID:  familyID,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
		IP:        ip,
		UserAgent: truncate(userAgent, 200),
	}
	body, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, keyPrefixToken+hash, body, ttl)
	pipe.SAdd(ctx, keyPrefixFamily+familyID, hash)
	// The family index must outlive its tokens, or a revocation arriving
	// after the last rotation would have nothing to revoke.
	pipe.Expire(ctx, keyPrefixFamily+familyID, ttl+24*time.Hour)
	pipe.SAdd(ctx, keyPrefixUser+userID, familyID)
	pipe.Expire(ctx, keyPrefixUser+userID, ttl+24*time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("storing refresh token: %w", err)
	}

	return &Issued{
		Token:     token,
		SessionID: sessionID,
		FamilyID:  familyID,
		ExpiresAt: rec.ExpiresAt,
	}, nil
}

// Extend lengthens a token's life.
//
// Rotation happens before the account kind is known -- the refresh token is
// opaque and the record is what identifies its owner -- so a farmer's longer
// session is applied here once it is.
func (s *Sessions) Extend(ctx context.Context, token string, ttl time.Duration) error {
	hash := hashToken(token)

	raw, err := s.rdb.Get(ctx, keyPrefixToken+hash).Result()
	if errors.Is(err, redis.Nil) {
		return ErrRefreshUnknown
	}
	if err != nil {
		return fmt.Errorf("reading refresh token: %w", err)
	}

	var rec refreshRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return fmt.Errorf("decoding refresh record: %w", err)
	}
	rec.ExpiresAt = time.Now().UTC().Add(ttl)

	body, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, keyPrefixToken+hash, body, ttl)
	pipe.Expire(ctx, keyPrefixFamily+rec.FamilyID, ttl+24*time.Hour)
	pipe.Expire(ctx, keyPrefixUser+rec.UserID, ttl+24*time.Hour)
	_, err = pipe.Exec(ctx)
	return err
}

// Revoke ends one session, given its current refresh token. Signing out.
func (s *Sessions) Revoke(ctx context.Context, presented string) error {
	hash := hashToken(presented)

	raw, err := s.rdb.Get(ctx, keyPrefixToken+hash).Result()
	if errors.Is(err, redis.Nil) {
		// Already gone. Sign-out is idempotent by design: a client that
		// cannot sign out is a worse outcome than one that signs out twice.
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading refresh token: %w", err)
	}

	var rec refreshRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		return fmt.Errorf("decoding refresh record: %w", err)
	}
	return s.revokeFamily(ctx, rec.FamilyID)
}

// RevokeAllForUser ends every session a user holds. Used when an account is
// suspended, a password changes, or an administrator forces a sign-out.
func (s *Sessions) RevokeAllForUser(ctx context.Context, userID string) error {
	families, err := s.rdb.SMembers(ctx, keyPrefixUser+userID).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("listing sessions: %w", err)
	}
	for _, f := range families {
		if err := s.revokeFamily(ctx, f); err != nil {
			return err
		}
	}
	return s.rdb.Del(ctx, keyPrefixUser+userID).Err()
}

// CountForUser reports how many session families a user has open.
func (s *Sessions) CountForUser(ctx context.Context, userID string) (int64, error) {
	return s.rdb.SCard(ctx, keyPrefixUser+userID).Result()
}

func (s *Sessions) revokeFamily(ctx context.Context, familyID string) error {
	hashes, err := s.rdb.SMembers(ctx, keyPrefixFamily+familyID).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("listing family tokens: %w", err)
	}

	pipe := s.rdb.TxPipeline()
	for _, h := range hashes {
		pipe.Del(ctx, keyPrefixToken+h)
		// Tombstoned, not merely deleted: a stolen token presented after the
		// family was revoked should still be recognisable as a reuse.
		pipe.Set(ctx, keyPrefixUsed+h, familyID, 48*time.Hour)
	}
	pipe.Del(ctx, keyPrefixFamily+familyID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("revoking family: %w", err)
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
