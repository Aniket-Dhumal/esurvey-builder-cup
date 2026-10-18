package audit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// How fast the audit log can be written.
//
// Every request that touches a record writes a row here, and migration 0020
// chains each row to the one before it -- the trigger takes FOR UPDATE on the
// previous row, so inserts cannot overlap. This probe says what that costs.
// Set ESURVEY_SCALE=1.
func TestAuditWriteThroughput(t *testing.T) {
	if os.Getenv("ESURVEY_SCALE") == "" {
		t.Skip("set ESURVEY_SCALE=1 to measure audit throughput")
	}
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	defer pool.Close()
	ctx := context.Background()
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("no database: %v", err)
	}

	// Deliberately not cleaned up. Every row is chained to the one before it,
	// so deleting these would break the chain for every row that followed --
	// and `audit_log_verify()` would report the log as tampered with from here
	// on. Probe rows are a few thousand entries in a test database; a broken
	// chain is a test suite that can no longer tell tampering from tidying.

	rec := New(pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer func() { _ = rec.Close(ctx) }()

	for _, writers := range []int{1, 8, 32} {
		const each = 60
		var wg sync.WaitGroup
		began := time.Now()
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				for i := 0; i < each; i++ {
					// RecordSync: the path every write handler uses when the
					// entry must land before the answer goes out.
					if err := rec.RecordSync(ctx, Event{
						Action:       "scale.probe",
						Outcome:      OutcomeAllow,
						ActorLabel:   fmt.Sprintf("writer-%d", w),
						ResourceType: "crop_loss_report",
						ResourceID:   fmt.Sprintf("r-%d-%d", w, i),
					}); err != nil {
						t.Errorf("writer %d: %v", w, err)
						return
					}
				}
			}(w)
		}
		wg.Wait()
		took := time.Since(began)
		n := writers * each
		t.Logf("%2d concurrent writers: %d entries in %s -> %.0f/s (%.1f ms each)",
			writers, n, took.Round(time.Millisecond),
			float64(n)/took.Seconds(),
			float64(took.Milliseconds())/float64(n)*float64(writers))
	}
}
