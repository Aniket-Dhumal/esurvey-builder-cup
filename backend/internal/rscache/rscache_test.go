package rscache

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This cache is what makes the loss-estimation baselines affordable, so what
// is under test is mostly economics: that a settled window is bought once and
// kept, that a window which can still change is not, and that a broken row
// costs one analysis rather than failing forever.

func testCache(t *testing.T) *Store {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the cache tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("bad ESURVEY_TEST_DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 4
	cfg.MinConns = 0

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)

	clean := func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM remote_sensing_cache WHERE product LIKE 'test_%'`)
	}
	clean()
	t.Cleanup(clean)

	return New(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestWhatWentInComesBackOut(t *testing.T) {
	s := testCache(t)
	ctx := context.Background()

	s.Put(ctx, "test_p", "k1", "2023-06-15", "2023-08-14", []byte(`{"a":1}`), true)

	raw, ok := s.Get(ctx, "test_p", "k1", "2023-06-15", "2023-08-14")
	if !ok {
		t.Fatal("nothing came back")
	}
	if string(raw) != `{"a": 1}` && string(raw) != `{"a":1}` {
		t.Errorf("payload = %s", raw)
	}
}

func TestTheKeyIsTheWholeKey(t *testing.T) {
	// Product, key and both window ends. A cache that ignored the window
	// would answer a question about one season with another season's data,
	// which is the worst failure available to it: silently wrong, and
	// indistinguishable from working.
	s := testCache(t)
	ctx := context.Background()

	s.Put(ctx, "test_p", "k1", "2023-06-15", "2023-08-14", []byte(`{"a":1}`), true)

	for _, c := range []struct{ product, key, start, end string }{
		{"test_other", "k1", "2023-06-15", "2023-08-14"},
		{"test_p", "k2", "2023-06-15", "2023-08-14"},
		{"test_p", "k1", "2023-06-16", "2023-08-14"},
		{"test_p", "k1", "2023-06-15", "2023-08-15"},
	} {
		if _, ok := s.Get(ctx, c.product, c.key, c.start, c.end); ok {
			t.Errorf("%+v was served from a different entry", c)
		}
	}
}

func TestASettledWindowIsKeptIndefinitely(t *testing.T) {
	// Sentinel-2 passes from three years ago are not going to be re-flown, so
	// buying the same window twice buys nothing.
	s := testCache(t)
	ctx := context.Background()

	s.Put(ctx, "test_p", "settled", "2023-06-15", "2023-08-14", []byte(`{}`),
		Settled("2023-08-14"))

	var expires *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT expires_at FROM remote_sensing_cache
		WHERE product = 'test_p' AND cache_key = 'settled'`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires != nil {
		t.Errorf("a settled window expires at %v", *expires)
	}
}

func TestAWindowThatClosedYesterdayIsOnlyTrustedBriefly(t *testing.T) {
	// Products land and are reprocessed for a while after acquisition, so a
	// window that just closed can still gain a pass.
	yesterday := time.Now().AddDate(0, 0, -1).Format(dayLayout)
	s := testCache(t)
	ctx := context.Background()

	if Settled(yesterday) {
		t.Fatal("a window that closed yesterday is reported as settled")
	}

	s.Put(ctx, "test_p", "fresh", "2026-01-01", yesterday, []byte(`{}`),
		Settled(yesterday))

	var expires *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT expires_at FROM remote_sensing_cache
		WHERE product = 'test_p' AND cache_key = 'fresh'`).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	if expires == nil {
		t.Fatal("it was kept as permanent")
	}
	if expires.After(time.Now().Add(FreshWindowTTL + time.Hour)) {
		t.Errorf("expires at %v, beyond the fresh-window TTL", *expires)
	}
}

func TestAnUnparseableWindowIsNeverKeptForever(t *testing.T) {
	if Settled("not-a-date") {
		t.Error("a window with no readable end was treated as settled")
	}
}

func TestAnExpiredEntryIsNotServed(t *testing.T) {
	s := testCache(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO remote_sensing_cache
			(product, cache_key, window_start, window_end, result, expires_at)
		VALUES ('test_p', 'expired', '2026-01-01', '2026-03-01', '{}',
		        now() - interval '1 hour')`); err != nil {
		t.Fatal(err)
	}

	if _, ok := s.Get(ctx, "test_p", "expired", "2026-01-01", "2026-03-01"); ok {
		t.Error("an expired entry was served")
	}
}

func TestTheCacheCountsWhatItSaved(t *testing.T) {
	// A cache that silently misses every time looks exactly like one that
	// works, right up until the invoice arrives.
	s := testCache(t)
	ctx := context.Background()

	s.Put(ctx, "test_p", "hits", "2023-06-15", "2023-08-14", []byte(`{}`), true)
	for i := 0; i < 3; i++ {
		if _, ok := s.Get(ctx, "test_p", "hits", "2023-06-15", "2023-08-14"); !ok {
			t.Fatal("a stored entry was not served")
		}
	}

	var hits int
	if err := s.pool.QueryRow(ctx, `
		SELECT hits FROM remote_sensing_cache
		WHERE product = 'test_p' AND cache_key = 'hits'`).Scan(&hits); err != nil {
		t.Fatal(err)
	}
	if hits != 3 {
		t.Errorf("hits = %d, want 3", hits)
	}
}

func TestStatsReportWhatIsHeld(t *testing.T) {
	s := testCache(t)
	ctx := context.Background()

	s.Put(ctx, "test_p", "a", "2023-06-15", "2023-08-14", []byte(`{}`), true)
	s.Put(ctx, "test_p", "b", "2023-06-15", "2023-08-14", []byte(`{}`), false)

	c, err := s.CacheStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Rows < 2 {
		t.Errorf("rows = %d", c.Rows)
	}
	if c.Permanent < 1 || c.Expiring < 1 {
		t.Errorf("permanent = %d, expiring = %d; both kinds should be counted",
			c.Permanent, c.Expiring)
	}
}

func TestForgetRemovesOneEntryAndOnlyThatOne(t *testing.T) {
	s := testCache(t)
	ctx := context.Background()

	s.Put(ctx, "test_p", "keep", "2023-06-15", "2023-08-14", []byte(`{}`), true)
	s.Put(ctx, "test_p", "drop", "2023-06-15", "2023-08-14", []byte(`{}`), true)

	s.Forget(ctx, "test_p", "drop", "2023-06-15", "2023-08-14")

	if _, ok := s.Get(ctx, "test_p", "drop", "2023-06-15", "2023-08-14"); ok {
		t.Error("a forgotten entry was still served")
	}
	if _, ok := s.Get(ctx, "test_p", "keep", "2023-06-15", "2023-08-14"); !ok {
		t.Error("forgetting one entry removed another")
	}
}

func TestSweepKeepsWhatCannotChange(t *testing.T) {
	// A settled window is kept because buying it again would cost money to
	// learn the same thing.
	s := testCache(t)
	ctx := context.Background()

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO remote_sensing_cache
			(product, cache_key, window_start, window_end, result, expires_at)
		VALUES
			('test_p', 'permanent', '2023-06-15', '2023-08-14', '{}', NULL),
			('test_p', 'stale', '2023-06-15', '2023-08-14', '{}',
			 now() - interval '1 day')`); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	var kept, dropped int
	_ = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM remote_sensing_cache
		WHERE product = 'test_p' AND cache_key = 'permanent'`).Scan(&kept)
	_ = s.pool.QueryRow(ctx, `
		SELECT count(*) FROM remote_sensing_cache
		WHERE product = 'test_p' AND cache_key = 'stale'`).Scan(&dropped)

	if kept != 1 {
		t.Error("a settled window was swept")
	}
	if dropped != 0 {
		t.Error("an expired row survived the sweep")
	}
}

func TestAReplacementOverwritesRatherThanFails(t *testing.T) {
	// Two assessments racing for the same neighbourhood is the normal case
	// after a district-wide event, and neither should fail because the other
	// got there first.
	s := testCache(t)
	ctx := context.Background()

	s.Put(ctx, "test_p", "race", "2023-06-15", "2023-08-14", []byte(`{"v":1}`), true)
	s.Put(ctx, "test_p", "race", "2023-06-15", "2023-08-14", []byte(`{"v":2}`), true)

	raw, ok := s.Get(ctx, "test_p", "race", "2023-06-15", "2023-08-14")
	if !ok {
		t.Fatal("nothing stored")
	}
	if string(raw) != `{"v": 2}` && string(raw) != `{"v":2}` {
		t.Errorf("payload = %s, want the later write", raw)
	}
}

func TestWithoutADatabaseNothingBreaks(t *testing.T) {
	// A cache failure should cost money, not correctness.
	s := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	s.Put(ctx, "test_p", "k", "2023-06-15", "2023-08-14", []byte(`{}`), true)
	if _, ok := s.Get(ctx, "test_p", "k", "2023-06-15", "2023-08-14"); ok {
		t.Error("a store with no database served something")
	}
	s.Forget(ctx, "test_p", "k", "2023-06-15", "2023-08-14")

	if _, err := s.Sweep(ctx); err == nil {
		t.Error("sweeping with no database reported success")
	}
	if _, err := s.CacheStats(ctx); err == nil {
		t.Error("statistics with no database reported success")
	}
}
