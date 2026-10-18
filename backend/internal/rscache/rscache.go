package rscache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// The cache in front of the analytics engine.
//
// # Why this is not an optimisation
//
// Every analysis is billed. Reading three prior seasons for a parcel turns
// one call into four, and doing it per assessment re-derives an answer that
// cannot have changed: Sentinel-2 passes from 2023 are not going to be
// re-flown. Without this, the parameters that give the damage figure a
// denominator would cost four times as much as the figure itself, every time,
// forever.
//
// # Why some rows never expire and others do
//
// A window that closed weeks ago is settled: the archive holds what it holds.
// A window that ended yesterday can still gain a pass as products land, so a
// result over it is provisional and is trusted only briefly. Treating both
// the same would either throw away permanent answers or serve stale ones.
//
// # Why the engine is asked at most once for a thing nobody has asked for
//
// A neighbourhood is keyed by grid cell, not by parcel. In the village-wide
// hailstorm this platform exists for, four hundred farmers file against the
// same square kilometre in the same fortnight; without this they would buy
// four hundred identical analyses.

// Store is the cache.
type Store struct {
	pool *db.Pool
	log  *slog.Logger
}

func New(pool *db.Pool, log *slog.Logger) *Store {
	return &Store{pool: pool, log: log}
}

// ArchiveSettleDays is how long after a window closes its answer stops
// changing. Exported because callers decide what "settled" means for their
// own product.
const ArchiveSettleDays = 21

// FreshWindowTTL is how long an unsettled result is trusted.
const FreshWindowTTL = 3 * 24 * time.Hour

const dayLayout = "2006-01-02"

// Get returns a cached payload.
func (s *Store) Get(
	ctx context.Context, product, key, start, end string,
) ([]byte, bool) {
	if s.pool == nil {
		return nil, false
	}

	var raw []byte
	err := s.pool.QueryRow(ctx, `
		UPDATE remote_sensing_cache
		SET hits = hits + 1
		WHERE product = $1 AND cache_key = $2
		  AND window_start = $3::date AND window_end = $4::date
		  AND (expires_at IS NULL OR expires_at > now())
		RETURNING result`,
		product, key, start, end).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false
	}
	if err != nil {
		s.log.Warn("reading the remote sensing cache",
			"product", product, "error", err)
		return nil, false
	}

	return raw, true
}

// Put stores a payload, permanently when the window can no longer change.
func (s *Store) Put(
	ctx context.Context, product, key, start, end string, raw []byte, permanent bool,
) {
	if s.pool == nil {
		return
	}

	var expires any
	if !permanent {
		expires = time.Now().Add(FreshWindowTTL)
	}

	// Written outside any caller transaction, and a conflict is a no-op:
	// two assessments racing for the same neighbourhood is the normal case
	// after a district-wide event, and neither should fail because the other
	// got there first.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO remote_sensing_cache
			(product, cache_key, window_start, window_end, result, expires_at)
		VALUES ($1, $2, $3::date, $4::date, $5, $6)
		ON CONFLICT (product, cache_key, window_start, window_end)
		DO UPDATE SET result = EXCLUDED.result,
		              computed_at = now(),
		              expires_at = EXCLUDED.expires_at`,
		product, key, start, end, raw, expires)
	if err != nil {
		s.log.Warn("writing to the remote sensing cache",
			"product", product, "error", err)
	}
}

// Settled reports whether a window's answer can still change.
func Settled(end string) bool {
	t, err := time.Parse(dayLayout, end)
	if err != nil {
		// An unparseable date is not something to keep forever.
		return false
	}
	return time.Since(t) > ArchiveSettleDays*24*time.Hour
}

// CacheStats is what the cache is actually doing, for the readiness endpoint.
//
// Reported because a cache that silently misses every time looks exactly like
// one that is working, right up until the invoice.
type CacheStats struct {
	Rows      int    `json:"rows"`
	Hits      int    `json:"hits"`
	Permanent int    `json:"permanent"`
	Expiring  int    `json:"expiring"`
	Oldest    string `json:"oldest,omitempty"`
}

func (s *Store) CacheStats(ctx context.Context) (*CacheStats, error) {
	if s.pool == nil {
		return nil, errors.New("no database")
	}
	var c CacheStats
	err := s.pool.QueryRow(ctx, `
		SELECT count(*),
		       COALESCE(sum(hits), 0),
		       count(*) FILTER (WHERE expires_at IS NULL),
		       count(*) FILTER (WHERE expires_at IS NOT NULL),
		       COALESCE(to_char(min(computed_at), 'YYYY-MM-DD"T"HH24:MI:SSZ'), '')
		FROM remote_sensing_cache`).
		Scan(&c.Rows, &c.Hits, &c.Permanent, &c.Expiring, &c.Oldest)
	if err != nil {
		return nil, fmt.Errorf("reading cache statistics: %w", err)
	}
	return &c, nil
}

// Sweep removes expired rows.
//
// Only the expiring ones: a settled window is kept because buying it again
// would cost money to learn the same thing. The table grows with the number
// of distinct parcels and localities analysed, which is bounded by the number
// of parcels in Maharashtra.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	if s.pool == nil {
		return 0, errors.New("no database")
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM remote_sensing_cache
		WHERE expires_at IS NOT NULL AND expires_at < now()`)
	if err != nil {
		return 0, fmt.Errorf("sweeping the cache: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Forget removes one entry.
//
// Used when a stored payload cannot be decoded: a row that fails to parse
// would otherwise fail on every read for as long as it is kept.
func (s *Store) Forget(ctx context.Context, product, key, start, end string) {
	if s.pool == nil {
		return
	}
	_, _ = s.pool.Exec(ctx, `
		DELETE FROM remote_sensing_cache
		WHERE product = $1 AND cache_key = $2
		  AND window_start = $3::date AND window_end = $4::date`,
		product, key, start, end)
}
