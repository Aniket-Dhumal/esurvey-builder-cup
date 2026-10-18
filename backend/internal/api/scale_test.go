package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/idempotency"
)

// The idempotency record at the volume of a season, under the concurrency of
// the morning after an event. Set ESURVEY_SCALE=1.

func scalePool(t *testing.T, conns int32) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("ESURVEY_SCALE") == "" {
		t.Skip("set ESURVEY_SCALE=1 to run the scale probes")
	}
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	// What production runs with: ESURVEY_DB_MAX_CONNS defaults to 20.
	cfg.MaxConns = conns
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func percentile(d []time.Duration, p float64) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[int(float64(len(d)-1)*p)]
}

func TestIdempotencyAtSeasonVolume(t *testing.T) {
	pool := scalePool(t, 20)
	ctx := context.Background()
	store := idempotency.New(pool)

	const prefix = "scale-"
	_, _ = pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE key LIKE 'scale-%'`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM idempotency_keys WHERE key LIKE 'scale-%'`)
	})

	// A season: a million answered keys across a hundred thousand farmers, a
	// third of them past retention.
	const volume = 1_000_000
	seed := time.Now()
	if _, err := pool.Exec(ctx, `
		INSERT INTO idempotency_keys (principal_id, key, method, route, status,
		                              response_body, content_type, created_at,
		                              completed_at)
		SELECT md5((g % 100000)::text)::uuid, 'scale-' || lpad(g::text, 8, '0'), 'POST',
		       'POST /api/v1/reports', 201,
		       convert_to(repeat('x', 300), 'UTF8'), 'application/json',
		       now() - ((g % 45) || ' days')::interval,
		       now() - ((g % 45) || ' days')::interval
		  FROM generate_series(1, $1) g`, volume); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	_, _ = pool.Exec(ctx, `ANALYZE idempotency_keys`)
	t.Logf("seeded %d keys in %s", volume, time.Since(seed).Round(time.Millisecond))

	var size string
	_ = pool.QueryRow(ctx, `SELECT pg_size_pretty(pg_total_relation_size('idempotency_keys'))`).Scan(&size)
	t.Logf("idempotency_keys on disk: %s", size)

	// The morning after: 400 handsets at once against a pool of 20, each
	// claiming and completing a key.
	const workers, perWorker = 400, 25
	var mu sync.Mutex
	var begins, completes []time.Duration
	var failures int
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			principal := fmt.Sprintf("00000000-0000-4000-8000-%012d", w)
			for i := 0; i < perWorker; i++ {
				key := fmt.Sprintf("%sw%d-%d", prefix, w, i)
				b := time.Now()
				_, err := store.Begin(ctx, principal, key, "POST", "POST /api/v1/reports", "")
				bd := time.Since(b)
				c := time.Now()
				if err == nil {
					err = store.Complete(ctx, principal, key, 201, "application/json", []byte(`{"id":"x"}`))
				}
				cd := time.Since(c)
				mu.Lock()
				begins = append(begins, bd)
				completes = append(completes, cd)
				if err != nil {
					failures++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	elapsed := time.Since(start)
	n := workers * perWorker
	t.Logf("%d claim+complete from %d concurrent callers in %s: %.0f/s",
		n, workers, elapsed.Round(time.Millisecond), float64(n)/elapsed.Seconds())
	t.Logf("Begin p50 %s p99 %s; Complete p50 %s p99 %s; failures %d",
		percentile(begins, .5), percentile(begins, .99),
		percentile(completes, .5), percentile(completes, .99), failures)

	sweep := time.Now()
	removed, err := store.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	t.Logf("sweep removed %d expired keys in %s", removed, time.Since(sweep).Round(time.Millisecond))
}

// The console's health page must not read every handset.
//
// It counted and summed across the whole table per load; at a lakh of phones
// that is a sequential scan on the pool the write path shares
// (docs/SYNC_SCALE_REVIEW.md, M7).
func TestDeviceHealthAtFleetScale(t *testing.T) {
	pool := scalePool(t, 8)
	ctx := context.Background()
	srv := &Server{DB: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	_, _ = pool.Exec(ctx, `DELETE FROM device_sync_status WHERE device_id LIKE 'fleet-%'`)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM device_sync_status WHERE device_id LIKE 'fleet-%'`)
	})

	// A district's fleet: most phones have nothing waiting, a few per cent do,
	// and a handful are stuck or carrying refusals.
	const fleet = 300000
	seed := time.Now()
	if _, err := pool.Exec(ctx, `
		INSERT INTO device_sync_status (
			device_id, principal_id, app_version, platform,
			pending_reports, pending_captures, pending_boundaries,
			pending_appeals, failed_items, pending_bytes,
			oldest_pending_at, last_success_at, reported_at)
		SELECT 'fleet-' || lpad(g::text, 10, '0'),
		       md5((g % 50000)::text)::uuid, '1.1.0', 'android',
		       CASE WHEN g % 40 = 0 THEN 1 ELSE 0 END,
		       CASE WHEN g % 40 = 0 THEN 4 ELSE 0 END,
		       0, 0,
		       CASE WHEN g % 500 = 0 THEN 1 ELSE 0 END,
		       CASE WHEN g % 40 = 0 THEN 12000000 ELSE 0 END,
		       CASE WHEN g % 40 = 0
		            THEN now() - ((g % 72) || ' hours')::interval END,
		       now() - ((g % 96) || ' hours')::interval,
		       now() - ((g % 96) || ' hours')::interval
		  FROM generate_series(1, $1) g`, fleet); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	_, _ = pool.Exec(ctx, `ANALYZE device_sync_status`)
	t.Logf("seeded %d devices in %s", fleet, time.Since(seed).Round(time.Millisecond))

	var slowest time.Duration
	for i := 0; i < 5; i++ {
		began := time.Now()
		body, err := srv.computeDeviceHealth(ctx)
		took := time.Since(began)
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		if len(body) == 0 {
			t.Fatal("no health payload")
		}
		if took > slowest {
			slowest = took
		}
	}
	t.Logf("device health over %d devices: %s (slowest of 5)", fleet,
		slowest.Round(time.Millisecond))
	if slowest > 500*time.Millisecond {
		t.Errorf("the health page took %s; it reads indexes, not the table", slowest)
	}
}
