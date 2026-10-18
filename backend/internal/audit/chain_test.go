package audit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tamper-evidence for the audit log.
//
// The append-only trigger stops the application rewriting its own history. It
// stops nothing done with database privileges -- and the people holding those
// are the people this log exists to hold accountable. So every entry hashes
// over the one before it, and these tests are about whether that actually
// catches the three things somebody would do.

func chainPool(t *testing.T, seed string) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the audit chain tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("bad ESURVEY_TEST_DATABASE_URL: %v", err)
	}
	cfg.MaxConns = 2
	cfg.MinConns = 0
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	// The same way the real pool supplies it.
	cfg.ConnConfig.RuntimeParams["esurvey.audit_seed"] = seed

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
		bg := context.Background()
		_, _ = pool.Exec(bg, `ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_rewrite`)
		_, _ = pool.Exec(bg, `DELETE FROM audit_log WHERE action LIKE 'chaintest.%'`)
		_, _ = pool.Exec(bg, `ALTER TABLE audit_log ENABLE TRIGGER audit_log_no_rewrite`)
	}
	clean()
	t.Cleanup(clean)

	return pool
}

// write appends entries and returns the id of the first.
func write(t *testing.T, pool *pgxpool.Pool, labels ...string) int64 {
	t.Helper()
	var first int64
	for i, label := range labels {
		var id int64
		if err := pool.QueryRow(context.Background(), `
			INSERT INTO audit_log (action, outcome, actor_label)
			VALUES ('chaintest.entry', 'allow', $1) RETURNING id`,
			label).Scan(&id); err != nil {
			t.Fatalf("writing entry %d: %v", i, err)
		}
		if i == 0 {
			first = id
		}
	}
	return first
}

// verify runs the database's own verification and returns the first break.
func verify(t *testing.T, pool *pgxpool.Pool, from int64) (int64, string, bool) {
	t.Helper()
	requireNoOtherWriters(t, pool, from)
	rows, err := pool.Query(context.Background(),
		`SELECT broken_at, reason FROM audit_log_verify($1)`, from)
	if err != nil {
		t.Fatalf("verifying: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return 0, "", false
	}
	var at int64
	var reason string
	if err := rows.Scan(&at, &reason); err != nil {
		t.Fatalf("reading the verdict: %v", err)
	}
	return at, reason, true
}

// asDBA does what somebody with database privileges can do: turn the
// append-only trigger off, change history, turn it back on. The table
// afterwards looks exactly as it did before -- which is the whole reason the
// chain exists.
func asDBA(t *testing.T, pool *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`ALTER TABLE audit_log DISABLE TRIGGER audit_log_no_rewrite`); err != nil {
		t.Fatalf("disabling the trigger: %v", err)
	}
	defer func() {
		if _, err := pool.Exec(ctx,
			`ALTER TABLE audit_log ENABLE TRIGGER audit_log_no_rewrite`); err != nil {
			t.Fatalf("re-enabling the trigger: %v", err)
		}
	}()
	if _, err := pool.Exec(ctx, statement, args...); err != nil {
		t.Fatalf("tampering: %v", err)
	}
}

func TestAnUntouchedLogVerifies(t *testing.T) {
	// The test that keeps the others honest. If verification reported a break
	// on an intact log, every test below would pass for the wrong reason.
	pool := chainPool(t, "chain-test-seed")
	first := write(t, pool, "alice", "bob", "carol")

	if at, reason, broken := verify(t, pool, first); broken {
		t.Fatalf("an untouched log reported a break at %d: %s", at, reason)
	}
}

func TestEveryEntryLinksToTheOneBefore(t *testing.T) {
	// The property the detection rests on. Without it the "chain" is a column
	// of unrelated hashes and changing a row breaks nothing.
	pool := chainPool(t, "chain-test-seed")
	first := write(t, pool, "alice", "bob", "carol")

	rows, err := pool.Query(context.Background(), `
		SELECT id, prev_hash, entry_hash FROM audit_log
		WHERE id >= $1 ORDER BY id`, first)
	if err != nil {
		t.Fatalf("reading the chain: %v", err)
	}
	defer rows.Close()

	var prevEntry string
	var n int
	for rows.Next() {
		var id int64
		var prev, entry string
		if err := rows.Scan(&id, &prev, &entry); err != nil {
			t.Fatalf("scanning: %v", err)
		}
		if entry == "" {
			t.Fatalf("entry %d has no hash", id)
		}
		if n > 0 && prev != prevEntry {
			t.Errorf("entry %d links to %q, but the entry before it hashed to %q",
				id, prev, prevEntry)
		}
		prevEntry = entry
		n++
	}
	if n != 3 {
		t.Fatalf("read %d entries, wrote 3", n)
	}
}

func TestChangingAnEntryIsDetected(t *testing.T) {
	// An officer's denial rewritten into an approval, months later, by
	// somebody with database access. This is the case the chain is for.
	pool := chainPool(t, "chain-test-seed")
	first := write(t, pool, "alice", "bob", "carol")

	asDBA(t, pool,
		`UPDATE audit_log SET outcome = 'deny', actor_label = 'not-bob'
		 WHERE action = 'chaintest.entry' AND actor_label = 'bob'`)

	var edited int64
	if err := pool.QueryRow(context.Background(), `
		SELECT id FROM audit_log
		 WHERE action = 'chaintest.entry' AND actor_label = 'not-bob'`).
		Scan(&edited); err != nil {
		t.Fatal(err)
	}

	at, reason, broken := verify(t, pool, first)
	if !broken {
		t.Fatal("an edited entry verified clean; the log is not evidence")
	}
	if at != edited {
		t.Errorf("the break is reported at %d, the edited entry is %d", at, edited)
	}
	t.Logf("detected: %s", reason)
}

func TestRemovingAnEntryIsDetected(t *testing.T) {
	// The subtler attack: not changing what happened, but deleting the record
	// that it did. Append-only alone would leave nothing to notice.
	pool := chainPool(t, "chain-test-seed")
	first := write(t, pool, "alice", "bob", "carol")

	asDBA(t, pool,
		`DELETE FROM audit_log WHERE action = 'chaintest.entry' AND actor_label = 'bob'`)

	at, reason, broken := verify(t, pool, first)
	if !broken {
		t.Fatal("a deleted entry left the chain verifying clean")
	}
	t.Logf("detected at %d: %s", at, reason)
}

func TestTheLogCannotBeVerifiedWithoutTheSeed(t *testing.T) {
	// Otherwise somebody who can write rows can also recompute a chain over
	// rows they invented, and verification would confirm their forgery.
	//
	// Reported as a break rather than as success is the correct outcome:
	// without the seed you cannot verify this log, and you should not be told
	// that you can.
	// Both connections are opened before anything is written: opening one
	// clears the test's own entries, and doing that after writing would leave
	// nothing to verify and pass for the wrong reason.
	pool := chainPool(t, "chain-test-seed")
	wrong := chainPool(t, "a-different-seed")

	first := write(t, pool, "alice", "bob")

	if _, _, broken := verify(t, pool, first); broken {
		t.Fatal("the correct seed could not verify the log it wrote")
	}
	if _, _, broken := verify(t, wrong, first); !broken {
		t.Fatal("the log verified under a seed it was not written with")
	}
}

// Where an entry happened is part of what it says, once it is recorded by id
// (migration 0044): it verifies when untouched, and moving it breaks the chain.
func TestTheJurisdictionIDIsCoveredByTheChain(t *testing.T) {
	pool := chainPool(t, "chain-test-seed")
	ctx := context.Background()

	var jur string
	if err := pool.QueryRow(ctx,
		`SELECT id FROM jurisdictions ORDER BY id LIMIT 1`).Scan(&jur); err != nil {
		t.Skipf("no jurisdiction to place an entry in: %v", err)
	}
	legacy := write(t, pool, "before")
	var placed int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO audit_log (action, outcome, actor_label, jurisdiction_id)
		VALUES ('chaintest.entry', 'allow', 'placed', $1) RETURNING id`, jur).
		Scan(&placed); err != nil {
		t.Fatalf("writing a placed entry: %v", err)
	}
	write(t, pool, "after")

	if at, reason, broken := verify(t, pool, legacy); broken {
		t.Fatalf("a log mixing entries with and without a place broke at %d: %s", at, reason)
	}

	asDBA(t, pool, `UPDATE audit_log SET jurisdiction_id = gen_random_uuid() WHERE id = $1`, placed)
	at, _, broken := verify(t, pool, legacy)
	if !broken || at != placed {
		t.Fatalf("moving an entry to another place was not detected (broken=%v at %d, want %d)",
			broken, at, placed)
	}
}

// Entries written at the same instant chain one after another, not side by
// side. Three assessment workers finishing together all chained onto the same
// predecessor, and verification then reported the honest log as tampered
// with -- the one outcome an audit chain must never produce.
func TestConcurrentEntriesDoNotForkTheChain(t *testing.T) {
	pool := chainPool(t, "chain-test-seed")
	ctx := context.Background()

	first := write(t, pool, "start")

	const writers, each = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, writers*each)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := pool.Exec(ctx, `
					INSERT INTO audit_log (action, outcome, actor_label)
					VALUES ('chaintest.entry', 'allow', $1)`,
					fmt.Sprintf("writer-%d-%d", w, i)); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a concurrent write failed: %v", err)
	}

	if at, reason, broken := verify(t, pool, first); broken {
		t.Fatalf("concurrent writes forked the chain at %d: %s", at, reason)
	}
}

// requireNoOtherWriters skips when something else wrote into the range being
// verified.
//
// The chain is one sequence over the whole table, and the hash covers a seed
// taken from the *writing session's* settings -- the same way the real pool
// supplies it. So a row written by another connection under another seed
// cannot be recomputed here, and audit_log_verify reports it as "does not hash
// to what it recorded": a fork in an honest log.
//
// That is not a finding about the chain, it is a finding about the database
// being shared. On a developer's machine the live stack is usually up and
// writing its own entries, and `go test ./...` runs this package alongside
// others that exercise the API. Under those conditions no assertion in this
// file is sound -- not the ones expecting an intact chain, and not the ones
// expecting a break at a particular row, which a foreign row can shadow.
//
// So it is a skip with the reason printed, never a pass: a green run here has
// to mean the chain was actually verified.
func requireNoOtherWriters(t *testing.T, pool *pgxpool.Pool, from int64) {
	t.Helper()
	var foreign int64
	var action string
	err := pool.QueryRow(context.Background(), `
		SELECT count(*), coalesce(min(action), '')
		  FROM audit_log
		 WHERE id >= $1 AND action NOT LIKE 'chaintest.%'`, from).
		Scan(&foreign, &action)
	if err != nil {
		t.Fatalf("looking for other writers: %v", err)
	}
	if foreign > 0 {
		t.Skipf("%d entries from another writer landed in the range being "+
			"verified (first: %s). The chain's hash covers the writing "+
			"session's seed, so entries this test did not write cannot be "+
			"recomputed here. Run this package alone, against a database no "+
			"live service is writing to.", foreign, action)
	}
}
