package db

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrations are the one thing no developer's machine can verify.
//
// Every existing database was migrated incrementally, one file at a time as
// each was written, so it would pass any check by accident. What has never
// been established is that the set applies *in order, to an empty database*
// — which is what a new environment, a restore from backup, and a fresh CI
// run all do.
//
// These run against a real PostGIS instance and skip without one.

func testPool(t *testing.T) *Pool {
	t.Helper()

	// Its own variable, not the one the other suites use.
	//
	// These tests DROP SCHEMA public. Sharing a database with the listing
	// suites means dropping the schema out from under them mid-run -- which
	// is exactly what happened the first time `go test ./...` ran them
	// together, and it surfaced as "relation jurisdictions does not exist" in
	// a package that never touches the schema.
	//
	// A separate variable makes that impossible rather than unlikely.
	url := os.Getenv("ESURVEY_TEST_MIGRATIONS_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_MIGRATIONS_URL (a throwaway database) to run the migration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Built from the URL directly rather than through config, so the test
	// needs nothing from the environment but the one variable it documents.
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("ESURVEY_TEST_DATABASE_URL is not a valid DSN: %v", err)
	}

	// These tests DROP SCHEMA public. Pointed at a development database that
	// destroys every report, capture and audit row in it, and the audit table
	// is append-only precisely because it is not supposed to be recoverable
	// by rewriting.
	//
	// So the database has to say it is disposable, in its own name. A
	// variable called ESURVEY_TEST_DATABASE_URL is not enough: it is one
	// paste away from a real DSN, and the paste is the likely mistake.
	if name := cfg.ConnConfig.Database; !strings.Contains(name, "test") {
		t.Fatalf(
			"refusing to run: these tests drop the public schema, and the "+
				"database is called %q. Point ESURVEY_TEST_MIGRATIONS_URL at a "+
				"database with \"test\" in its name.", name)
	}
	// Small on purpose: these run in parallel with the other database
	// suites against one Postgres, and the default is the core count.
	cfg.MaxConns = 4
	cfg.MinConns = 0

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Skipf("no database at ESURVEY_TEST_DATABASE_URL: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("no database at ESURVEY_TEST_DATABASE_URL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// freshSchema drops and recreates public, so each test starts from nothing.
func freshSchema(t *testing.T, pool *Pool) {
	t.Helper()
	ctx := context.Background()

	// postgis and its friends install into public. Dropping the schema takes
	// them with it, which is the point: an empty database is what a new
	// deployment actually has.
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS public CASCADE`,
		`CREATE SCHEMA public`,
		`GRANT ALL ON SCHEMA public TO public`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("resetting the schema: %v", err)
		}
	}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))
}

func TestMigrationsApplyToAnEmptyDatabase(t *testing.T) {
	pool := testPool(t)
	freshSchema(t, pool)

	ctx := context.Background()
	if err := Migrate(ctx, pool, quietLogger()); err != nil {
		t.Fatalf("migrations do not apply to an empty database: %v", err)
	}

	// Spot-check the tables every other package assumes exist.
	for _, table := range []string{
		"users", "user_roles", "jurisdictions", "assessments",
		"survey_events", "crop_loss_reports", "crop_loss_report_items",
		"report_assignments", "report_verifications", "report_adjudications",
		"survey_captures", "parcel_boundaries", "audit_log",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables
			                WHERE table_schema = 'public' AND table_name = $1)`,
			table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %q is missing after migration", table)
		}
	}
}

func TestMigratingTwiceIsANoOp(t *testing.T) {
	// Every container runs Migrate at startup, and a rolling deploy starts
	// several at once. A second pass that is not a no-op is a deploy that
	// fails on the second replica.
	pool := testPool(t)
	freshSchema(t, pool)

	ctx := context.Background()
	if err := Migrate(ctx, pool, quietLogger()); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	var afterFirst int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&afterFirst); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(ctx, pool, quietLogger()); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	var afterSecond int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&afterSecond); err != nil {
		t.Fatal(err)
	}
	if afterFirst != afterSecond {
		t.Errorf("a second pass applied %d more migrations", afterSecond-afterFirst)
	}
}

func TestEveryMigrationIsRecorded(t *testing.T) {
	pool := testPool(t)
	freshSchema(t, pool)

	ctx := context.Background()
	if err := Migrate(ctx, pool, quietLogger()); err != nil {
		t.Fatal(err)
	}

	files, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations are embedded")
	}

	var applied int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != len(files) {
		t.Errorf("%d migrations embedded, %d recorded", len(files), applied)
	}
}

func TestTheSearchIndexesExistAfterMigrating(t *testing.T) {
	// Migration 0008 is what keeps every search box from being a sequential
	// scan. It is also the one migration applied by hand to the running
	// database during development, so it is the most likely to be missing
	// from a fresh environment without anyone noticing -- the console would
	// work, and simply get slower every week.
	pool := testPool(t)
	freshSchema(t, pool)

	ctx := context.Background()
	if err := Migrate(ctx, pool, quietLogger()); err != nil {
		t.Fatal(err)
	}

	for _, idx := range []string{
		"crop_loss_reports_reference_trgm",
		"crop_loss_reports_farmer_name_trgm",
		"crop_loss_reports_village_name_trgm",
		"jurisdictions_name_trgm",
		"users_full_name_trgm",
		"audit_log_action_trgm",
	} {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM pg_indexes
			                WHERE schemaname = 'public' AND indexname = $1)`,
			idx).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("index %q is missing; that search is a sequential scan", idx)
		}
	}
}

func TestMigrationFilenamesAreOrdered(t *testing.T) {
	// Applied in filename order, so the order has to be unambiguous. This
	// needs no database.
	files, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	seen := map[int]string{}
	prev := 0
	for _, m := range files {
		if m.Version <= prev {
			t.Errorf("migration %d (%s) does not follow %d", m.Version, m.Name, prev)
		}
		if other, dup := seen[m.Version]; dup {
			t.Errorf("migrations %q and %q share version %d", other, m.Name, m.Version)
		}
		if strings.TrimSpace(m.SQL) == "" {
			t.Errorf("migration %d (%s) is empty", m.Version, m.Name)
		}
		if m.Checksum == "" {
			t.Errorf("migration %d (%s) has no checksum", m.Version, m.Name)
		}
		seen[m.Version] = m.Name
		prev = m.Version
	}
}
