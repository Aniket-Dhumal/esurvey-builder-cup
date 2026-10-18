package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Sign-in throttling.
//
// Counters live in Redis rather than in a users column. A failed sign-in
// would otherwise be a write to the identity table, which turns a password
// spray into database load, and the state is inherently short-lived: a TTL
// expires it correctly without a cleanup job that someone has to remember to
// schedule.
//
// Two independent limits, because they catch different attacks:
//
//   - per identifier, which stops one account being ground through a
//     dictionary;
//   - per source address, at a much higher threshold, which stops one client
//     spraying one password across many accounts.
//
// The address threshold is a multiple of the identifier one because a taluka
// office is a single NAT address with thirty officers behind it, and a limit
// tight enough to catch spraying would lock out the office every Monday.
const addressThresholdMultiplier = 10

// ErrLockedOut is returned when further attempts are refused.
var ErrLockedOut = errors.New("too many failed attempts")

// Throttle counts and limits failed authentication attempts.
type Throttle struct {
	rdb *redis.Client

	maxFailures int
	window      time.Duration
	lockFor     time.Duration
}

// NewThrottle builds a throttle.
func NewThrottle(rdb *redis.Client, maxFailures int, window, lockFor time.Duration) *Throttle {
	return &Throttle{rdb: rdb, maxFailures: maxFailures, window: window, lockFor: lockFor}
}

// Check reports whether an attempt may proceed.
//
// Both the identifier and the address are consulted before any password is
// verified, so a locked account costs an attacker a Redis read rather than an
// argon2 hash.
func (t *Throttle) Check(ctx context.Context, identifier, ip string) (time.Duration, error) {
	for _, key := range []string{lockKey("id", identifier), lockKey("ip", ip)} {
		if key == "" {
			continue
		}
		ttl, err := t.rdb.TTL(ctx, key).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return 0, fmt.Errorf("checking lockout: %w", err)
		}
		if ttl > 0 {
			return ttl, ErrLockedOut
		}
	}
	return 0, nil
}

// RecordFailure counts a failed attempt and locks out if the threshold is
// crossed. It returns the lock duration when one was applied.
func (t *Throttle) RecordFailure(ctx context.Context, identifier, ip string) (time.Duration, error) {
	locked := time.Duration(0)

	limits := []struct {
		kind, value string
		threshold   int
	}{
		{"id", identifier, t.maxFailures},
		{"ip", ip, t.maxFailures * addressThresholdMultiplier},
	}

	for _, l := range limits {
		if l.value == "" {
			continue
		}
		counter := failKey(l.kind, l.value)

		pipe := t.rdb.TxPipeline()
		incr := pipe.Incr(ctx, counter)
		// Expire on every increment, so the window slides forward with
		// activity rather than expiring mid-attack.
		pipe.Expire(ctx, counter, t.window)
		if _, err := pipe.Exec(ctx); err != nil {
			return 0, fmt.Errorf("counting failure: %w", err)
		}

		if int(incr.Val()) >= l.threshold {
			if err := t.rdb.Set(ctx, lockKey(l.kind, l.value), "1", t.lockFor).Err(); err != nil {
				return 0, fmt.Errorf("applying lockout: %w", err)
			}
			// Reset the counter so the lock expiring does not leave the
			// account one attempt from being locked again.
			t.rdb.Del(ctx, counter)
			locked = t.lockFor
		}
	}

	return locked, nil
}

// Reset clears the counters for an identifier. Called on a successful
// sign-in: a user who gets it right on the fourth try is not one attempt away
// from being locked out for the rest of the window.
func (t *Throttle) Reset(ctx context.Context, identifier, ip string) error {
	keys := []string{failKey("id", identifier), lockKey("id", identifier)}
	if ip != "" {
		// The address counter is deliberately not cleared. One user signing
		// in successfully says nothing about the other twenty-nine attempts
		// coming from the same address.
		_ = ip
	}
	return t.rdb.Del(ctx, keys...).Err()
}

// FailureCount reports the current count for an identifier, for diagnostics.
func (t *Throttle) FailureCount(ctx context.Context, identifier string) (int64, error) {
	n, err := t.rdb.Get(ctx, failKey("id", identifier)).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	return n, err
}

func failKey(kind, value string) string { return "authfail:" + kind + ":" + value }
func lockKey(kind, value string) string {
	if value == "" {
		return ""
	}
	return "authlock:" + kind + ":" + value
}
