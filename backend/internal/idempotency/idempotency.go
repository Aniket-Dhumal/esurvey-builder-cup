// Package idempotency makes a write safe to send again.
//
// # The protocol
//
// A client that may retry sends an Idempotency-Key header -- a value it chose
// and keeps for that one operation. The first request with a key executes and
// its answer is recorded. Every later request with the same key gets that
// answer back, marked Idempotency-Replayed, without executing again.
//
// This is the shape of the IETF httpapi draft for the header, with two choices
// made deliberately differently, both explained where they are made: the
// request body is not part of the match, and a 5xx is not recorded.
//
// # Why a key is claimed before the work, not recorded after it
//
// Two copies of the same retry can arrive together -- the app's foreground
// timer and the background worker, or a proxy that retried on its own. If the
// key were only written after the handler finished, both would execute. So the
// first to arrive claims the key with an INSERT that only one can win, and the
// other is told the first is still running.
package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Retention is how long an answer is kept.
//
// Thirty days. A reply lost in the field is retried when the phone next has
// signal, which for a farmer in a low-coverage village can be a week; a key
// swept before the retry arrives turns that retry into a second write.
const Retention = 30 * 24 * time.Hour

// StaleClaim is how long an unfinished claim is honoured.
//
// A claim is left unfinished when the process died mid-request. Past this, a
// retry may take the claim over and execute. The window is longer than the
// slowest write this API performs -- a video assembled from its chunks -- so a
// request that is merely slow is never executed twice.
const StaleClaim = 2 * time.Minute

// MaxBody is the largest answer recorded. Every write this API makes answers
// with a small JSON document; anything larger is recorded as completed with no
// body, and a replay then says so rather than silently returning nothing.
//
// 64 KiB, down from 512. A key is written for every report, photograph,
// boundary and appeal and kept thirty days; a million keys with ordinary
// bodies measured 503 MB (docs/SYNC_SCALE_REVIEW.md, M6). The largest answer
// a phone's write receives is a report of a few kilobytes.
const MaxBody = 64 << 10

// Errors a caller branches on.
var (
	// ErrInProgress means another request with this key is still running.
	ErrInProgress = errors.New("a request with this idempotency key is still in progress")
	// ErrMismatch means the key was first used for a different operation.
	ErrMismatch = errors.New("this idempotency key was used for a different request")
)

// Claim is the state of a key when a request arrives with it.
type Claim struct {
	// Fresh is true when this request owns the key and must execute.
	Fresh bool

	// Replay is set when the key already completed.
	Replay *Recorded
}

// Recorded is a stored answer.
type Recorded struct {
	Status      int
	ContentType string
	Body        []byte
	CompletedAt time.Time
}

// Store is the durable record.
type Store struct {
	pool *db.Pool
}

// New builds a store.
func New(pool *db.Pool) *Store { return &Store{pool: pool} }

// Begin claims a key or reports what it already did.
func (s *Store) Begin(
	ctx context.Context, principal, key, method, route, requestHash string,
) (Claim, error) {
	// One statement for the common case: nobody has used this key. The INSERT
	// either wins or conflicts, atomically.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO idempotency_keys (principal_id, key, method, route, request_hash)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (principal_id, key) DO NOTHING`,
		principal, key, method, route, requestHash)
	if err != nil {
		return Claim{}, fmt.Errorf("claiming an idempotency key: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return Claim{Fresh: true}, nil
	}

	var (
		storedMethod, storedRoute string
		status                    *int
		contentType               *string
		body                      []byte
		createdAt                 time.Time
		completedAt               *time.Time
	)
	err = s.pool.QueryRow(ctx, `
		SELECT method, route, status, content_type, response_body,
		       created_at, completed_at
		  FROM idempotency_keys
		 WHERE principal_id = $1 AND key = $2`,
		principal, key).Scan(&storedMethod, &storedRoute, &status, &contentType,
		&body, &createdAt, &completedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Swept between the INSERT and this read. Vanishingly rare; the
		// honest answer is to try the claim again.
		return s.Begin(ctx, principal, key, method, route, requestHash)
	}
	if err != nil {
		return Claim{}, fmt.Errorf("reading an idempotency key: %w", err)
	}

	if storedMethod != method || storedRoute != route {
		return Claim{}, ErrMismatch
	}

	if completedAt != nil && status != nil {
		rec := &Recorded{Status: *status, Body: body, CompletedAt: *completedAt}
		if contentType != nil {
			rec.ContentType = *contentType
		}
		return Claim{Replay: rec}, nil
	}

	// Unfinished. Honoured while it could still be running; taken over once
	// it cannot be, which is the state a crashed process leaves behind.
	if time.Since(createdAt) < StaleClaim {
		return Claim{}, ErrInProgress
	}
	tag, err = s.pool.Exec(ctx, `
		UPDATE idempotency_keys
		   SET created_at = now(), request_hash = NULLIF($3, '')
		 WHERE principal_id = $1 AND key = $2
		   AND completed_at IS NULL AND created_at = $4`,
		principal, key, requestHash, createdAt)
	if err != nil {
		return Claim{}, fmt.Errorf("taking over a stale idempotency key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Somebody else took it over first.
		return Claim{}, ErrInProgress
	}
	return Claim{Fresh: true}, nil
}

// Complete records the answer to a claimed key.
//
// A body larger than MaxBody is not stored; the status still is.
func (s *Store) Complete(
	ctx context.Context, principal, key string, status int, contentType string, body []byte,
) error {
	if len(body) > MaxBody {
		body = nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE idempotency_keys
		   SET status = $3, content_type = NULLIF($4, ''), response_body = $5,
		       completed_at = now()
		 WHERE principal_id = $1 AND key = $2 AND completed_at IS NULL`,
		principal, key, status, contentType, body)
	if err != nil {
		return fmt.Errorf("recording an idempotent answer: %w", err)
	}
	return nil
}

// Release forgets a claim so a retry executes again.
//
// Used when the answer was a server failure: a 5xx is a statement about the
// server at that moment, and replaying it would make one bad second permanent
// for that operation.
func (s *Store) Release(ctx context.Context, principal, key string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM idempotency_keys
		 WHERE principal_id = $1 AND key = $2 AND completed_at IS NULL`,
		principal, key)
	return err
}

// Sweep deletes answers older than Retention, in bounded batches.
//
// Batched so a season's worth of keys is removed without one long transaction
// holding locks the write path needs.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM idempotency_keys
			 WHERE ctid IN (
			     SELECT ctid FROM idempotency_keys
			      WHERE created_at < now() - $1::interval
			      LIMIT 5000)`,
			fmt.Sprintf("%d seconds", int(Retention.Seconds())))
		if err != nil {
			return total, fmt.Errorf("sweeping idempotency keys: %w", err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < 5000 {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
