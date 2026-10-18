// Package dbtest holds what the database-backed suites need from each other.
//
// # The problem
//
// Several packages share one test database, and `go test ./...` runs packages
// in parallel. That is fine while every suite only writes rows with its own
// prefix. It stops being fine when a suite reaches for DDL.
//
// Findings, adjudications and the audit log are append-only, enforced by a
// trigger. A fixture that has to delete its own rows therefore lifts the
// trigger, deletes, and puts it back — and `ALTER TABLE ... DISABLE TRIGGER`
// is table-wide and visible to every session, not scoped to the one that
// issued it. So for the moment one suite is clearing up, the guarantee is off
// for everybody.
//
// It showed up exactly as it would in production if this were production
// code: a test asserting that a recorded finding cannot be edited passed on
// its own and failed in the full run, because a different package had the
// trigger down while it ran. The assertion was right and the harness was
// lying.
//
// # The remedy
//
// One advisory lock, taken by every suite that turns a trigger off and by
// every test that depends on one being on. Postgres holds it per session, so
// the suites serialise against each other and against nothing else.
package dbtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// triggerLock is the advisory key. Arbitrary, and shared: any two callers
// using the same number exclude each other, which is the whole mechanism.
const triggerLock = 0x65737572_76657901 // "esurvey" + 1

// Serialise blocks until no other suite is lifting a trigger, and holds that
// state for the rest of the test.
//
// Takes a connection out of the pool and keeps it: an advisory lock belongs to
// the session that took it, so releasing the connection early would release
// the lock. Sized for that — the suites cap their pools well above one.
func Serialise(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring a connection for the suite lock: %v", err)
	}
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(triggerLock)); err != nil {
		conn.Release()
		t.Fatalf("taking the suite lock: %v", err)
	}

	// Registered first by every caller, so it runs last: the fixture's own
	// cleanup lifts the trigger again, and must finish inside the lock.
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(),
			`SELECT pg_advisory_unlock($1)`, int64(triggerLock))
		conn.Release()
	})
}
