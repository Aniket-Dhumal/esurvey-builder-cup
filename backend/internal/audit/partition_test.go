package audit

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The audit log is partitioned by month (migration 0037), which is what makes
// a year of it archivable rather than permanent. These are the properties that
// must survive that: entries still land, still chain, and the partition for a
// month nobody has reached yet can be made ahead of time.

func partitionPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the audit partition tests")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Skipf("no database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Skipf("no database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestTheAuditLogIsPartitionedByMonth(t *testing.T) {
	pool := partitionPool(t)
	ctx := context.Background()

	var partitioned bool
	if err := pool.QueryRow(ctx, `
		SELECT c.relkind = 'p' FROM pg_class c WHERE c.oid = 'audit_log'::regclass`).
		Scan(&partitioned); err != nil {
		t.Fatal(err)
	}
	if !partitioned {
		t.Fatal("audit_log is not partitioned; a year of it cannot be archived")
	}

	// An entry written now lands in this month's partition, not in a default
	// one -- a default partition would silently become the un-archivable table
	// this replaced.
	var into string
	if err := pool.QueryRow(ctx, `
		INSERT INTO audit_log (action, outcome, actor_label)
		VALUES ('partition.probe', 'allow', 'partition test')
		RETURNING tableoid::regclass::text`).Scan(&into); err != nil {
		t.Fatal(err)
	}
	want := "audit_log_" + time.Now().Format("2006_01")
	if into != want {
		t.Fatalf("an entry landed in %s; want %s", into, want)
	}
}

func TestAMonthAheadCanBeMadeBeforeItIsNeeded(t *testing.T) {
	pool := partitionPool(t)
	ctx := context.Background()

	// Housekeeping calls this; the log must never meet a month it has no
	// partition for.
	future := time.Now().AddDate(0, 3, 0)
	var name, again string
	if err := pool.QueryRow(ctx, `SELECT ensure_audit_partition($1)`, future).
		Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT ensure_audit_partition($1)`, future).
		Scan(&again); err != nil {
		t.Fatalf("making a partition twice failed: %v", err)
	}
	if name != again {
		t.Fatalf("two calls made %s and %s", name, again)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS `+name)
	})

	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatalf("partition %s was reported made and is not there", name)
	}
}

func TestTheAuditLogStillRefusesToBeRewritten(t *testing.T) {
	// The append-only trigger is declared on the partitioned parent; if it did
	// not reach the partitions, the log would quietly become editable.
	pool := partitionPool(t)
	ctx := context.Background()

	var id int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO audit_log (action, outcome) VALUES ('rewrite.probe', 'allow')
		RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE audit_log SET outcome = 'deny' WHERE id = $1`, id); err == nil {
		t.Fatal("an audit entry was rewritten")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, id); err == nil {
		t.Fatal("an audit entry was deleted")
	}
}
