package officeragent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Holding a document for a few minutes, and letting go of it.
//
// Everything worth asserting here is a limit: who can fetch a prepared document,
// how long it lasts, how many one officer may hold, how many bytes, and what a
// document too large to hold is told. None of it needs a database or a Redis --
// the policy is one piece of code over a small key-value interface, and these run
// it over the in-process backing with the clock in the test's hands. The Redis
// backing is three one-line methods over that same interface.

// preparedClock is a clock a test can move.
type preparedClock struct{ at time.Time }

func (c *preparedClock) now() time.Time { return c.at }

// testExports is a store whose time the test controls.
func testExports() (*Exports, *preparedClock) {
	clock := &preparedClock{at: time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)}
	return newExports(newMemoryKV(clock.now), clock.now), clock
}

// someExport is a finished document of a given size, with no claim behind it:
// what is under examination is the store, and BuildExport has its own tests.
func someExport(name string, size int) *Export {
	return &Export{
		Bytes:       bytes.Repeat([]byte("x"), size),
		ContentType: ContentType(FormatCSV),
		Filename:    name,
		Rows:        3,
		Fields:      []string{"reference"},
	}
}

const (
	preparingOfficer = "11111111-1111-1111-1111-111111111111"
	otherOfficer     = "22222222-2222-2222-2222-222222222222"
)

func TestAPreparedExportComesBackToTheOfficerWhoPreparedIt(t *testing.T) {
	store, _ := testExports()
	ctx := context.Background()

	p, err := store.Keep(ctx, preparingOfficer, FormatCSV,
		someExport("बार्शी claims.csv", 512))
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" || p.Rows != 3 || p.Bytes != 512 {
		t.Fatalf("what the console is told reads %+v", p)
	}

	got, err := store.Fetch(ctx, preparingOfficer, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Body) != 512 {
		t.Errorf("the document came back as %d bytes", len(got.Body))
	}
	if got.Filename != "बार्शी claims.csv" {
		t.Errorf("the filename did not survive: %q", got.Filename)
	}
	if got.ContentType != ContentType(FormatCSV) {
		t.Errorf("content type %q", got.ContentType)
	}
	if got.Format != string(FormatCSV) {
		t.Errorf("format %q", got.Format)
	}
}

func TestAnotherOfficerHoldingTheIdIsToldThereIsNoSuchExport(t *testing.T) {
	// The whole of the ownership rule, and it holds because the owner is in the
	// key rather than in a check somewhere after the lookup. The answer is the
	// same as expired and as never existed, deliberately: anything else would
	// tell an officer that a document exists which is not theirs.
	store, _ := testExports()
	ctx := context.Background()

	p, err := store.Keep(ctx, preparingOfficer, FormatXLSX,
		someExport("claims.xlsx", 2048))
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.Fetch(ctx, otherOfficer, p.ID)
	if !errors.Is(err, ErrNoPreparedExport) {
		t.Fatalf("another officer got %v", err)
	}
	if got != nil {
		t.Fatal("another officer was handed the document itself")
	}
}

func TestAPreparedExportIsGoneOnceItsTimeIsUp(t *testing.T) {
	store, clock := testExports()
	ctx := context.Background()

	p, err := store.Keep(ctx, preparingOfficer, FormatCSV,
		someExport("claims.csv", 64))
	if err != nil {
		t.Fatal(err)
	}

	clock.at = clock.at.Add(PreparedTTL - time.Second)
	if _, err := store.Fetch(ctx, preparingOfficer, p.ID); err != nil {
		t.Fatalf("gone a second before its time: %v", err)
	}

	clock.at = clock.at.Add(2 * time.Second)
	if _, err := store.Fetch(ctx, preparingOfficer, p.ID); !errors.Is(
		err, ErrNoPreparedExport) {
		t.Fatalf("still there after %s: %v", PreparedTTL, err)
	}
}

func TestTheOldestPreparedExportIsTheOneEvictedWhenTheCountIsReached(t *testing.T) {
	// A conversation that changed its mind: the document the officer asked for
	// first is the one they have already corrected.
	store, clock := testExports()
	ctx := context.Background()

	kept := make([]Prepared, 0, MaxPreparedPerOfficer+1)
	for i := 0; i < MaxPreparedPerOfficer+1; i++ {
		clock.at = clock.at.Add(time.Second)
		p, err := store.Keep(ctx, preparingOfficer, FormatCSV,
			someExport(fmt.Sprintf("claims-%d.csv", i), 64))
		if err != nil {
			t.Fatal(err)
		}
		kept = append(kept, p)
	}

	if _, err := store.Fetch(ctx, preparingOfficer, kept[0].ID); !errors.Is(
		err, ErrNoPreparedExport) {
		t.Errorf("the first document was not evicted: %v", err)
	}
	for _, p := range kept[1:] {
		if _, err := store.Fetch(ctx, preparingOfficer, p.ID); err != nil {
			t.Errorf("%s went with it: %v", p.Filename, err)
		}
	}
}

func TestTheByteCapEvictsOlderDocumentsToMakeRoom(t *testing.T) {
	// The cap that matters to the deployment: an officer asking for twelve
	// documents must not pin twelve documents' worth of a Redis sized for
	// sessions.
	store, clock := testExports()
	ctx := context.Background()

	// Three at two fifths of the budget each: the third cannot fit beside the
	// first two, and the first is what goes.
	size := (MaxPreparedBytesPerOfficer / 5) * 2
	var ids []Prepared
	for i := 0; i < 3; i++ {
		clock.at = clock.at.Add(time.Second)
		p, err := store.Keep(ctx, preparingOfficer, FormatPDF,
			someExport(fmt.Sprintf("statement-%d.pdf", i), size))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p)
	}

	if _, err := store.Fetch(ctx, preparingOfficer, ids[0].ID); !errors.Is(
		err, ErrNoPreparedExport) {
		t.Errorf("the oldest survived past the byte cap: %v", err)
	}
	for _, p := range ids[1:] {
		if _, err := store.Fetch(ctx, preparingOfficer, p.ID); err != nil {
			t.Errorf("%s was evicted as well: %v", p.Filename, err)
		}
	}
}

func TestADocumentLargerThanOneOfficerMayHoldIsRefusedWithBothNumbers(t *testing.T) {
	// Refused rather than stored by evicting everything else, because there
	// would be nothing left to evict. The sentence carries the remedy: fewer
	// columns, or fewer claims.
	store, _ := testExports()

	_, err := store.Keep(context.Background(), preparingOfficer, FormatPDF,
		someExport("everything.pdf", MaxPreparedBytesPerOfficer+1))

	var tooLarge PreparedTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("a document past the cap was accepted: %v", err)
	}
	if tooLarge.Limit != MaxPreparedBytesPerOfficer {
		t.Errorf("the refusal names %d as the limit", tooLarge.Limit)
	}
	for _, want := range []string{"fewer columns", "narrow"} {
		if !bytes.Contains([]byte(err.Error()), []byte(want)) {
			t.Errorf("the refusal does not say what to do: %q", err)
		}
	}
}

func TestADownloadLeavesThePreparedDocumentWhereItIs(t *testing.T) {
	// Not one-shot, and tested because it is a decision rather than an
	// oversight: a download interrupted on a taluka line is retried by the
	// browser, and one-shot would turn every retry into "that export is gone".
	store, _ := testExports()
	ctx := context.Background()

	p, err := store.Keep(ctx, preparingOfficer, FormatCSV,
		someExport("claims.csv", 128))
	if err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		got, err := store.Fetch(ctx, preparingOfficer, p.ID)
		if err != nil {
			t.Fatalf("fetch %d: %v", i+1, err)
		}
		if len(got.Body) != 128 {
			t.Fatalf("fetch %d came back as %d bytes", i+1, len(got.Body))
		}
	}
}

func TestAnIdThisCouldNotHaveIssuedNeverReachesAKey(t *testing.T) {
	// The id arrives as a path parameter, which is a caller speaking. Nothing a
	// caller sends should be concatenated into a key without having been looked
	// at first.
	store, _ := testExports()
	ctx := context.Background()

	for _, id := range []string{
		"", "..", "*", "agentexp:idx:" + preparingOfficer,
		preparingOfficer + ":anything", "ZZZZ",
		"0123456789abcdef0123456789abcdeg", // one byte that is not hex
		"0123456789abcdef0123456789abcde",  // one short
	} {
		if _, err := store.Fetch(ctx, preparingOfficer, id); !errors.Is(
			err, ErrNoPreparedExport) {
			t.Errorf("%q answered %v", id, err)
		}
	}
}

func TestAPreparedExportNeedsAnOfficerToBeFiledUnder(t *testing.T) {
	// A blank owner would file every such document under the same key, where
	// every other blank owner could read it.
	store, _ := testExports()

	if _, err := store.Keep(context.Background(), "", FormatCSV,
		someExport("claims.csv", 64)); err == nil {
		t.Fatal("a document was filed under nobody")
	}
	if _, err := store.Fetch(context.Background(), "",
		"0123456789abcdef0123456789abcdef"); !errors.Is(
		err, ErrNoPreparedExport) {
		t.Errorf("a blank owner fetched something: %v", err)
	}
}

// TestThePolicyHoldsOverTheBackingADeploymentUses needs a Redis and skips
// without one.
//
// Set ESURVEY_TEST_REDIS_ADDR (and ESURVEY_TEST_REDIS_PASSWORD), as the session
// and sign-in suites do; database 15, which those suites also use for test data.
//
// Everything above runs the same policy over the in-process backing, so what is
// left to find here is what only a server can go wrong at: that a document's
// bytes survive a round trip through a Redis value without being read as text,
// and that a lifetime set on a key is a lifetime Redis honours.
func TestThePolicyHoldsOverTheBackingADeploymentUses(t *testing.T) {
	addr := os.Getenv("ESURVEY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set ESURVEY_TEST_REDIS_ADDR to run the prepared export tests")
	}
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: os.Getenv("ESURVEY_TEST_REDIS_PASSWORD"),
		DB:       15,
	})
	t.Cleanup(func() { _ = rdb.Close() })

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("no redis: %v", err)
	}

	owner := "redis-" + fmt.Sprint(time.Now().UnixNano())
	store := NewRedisExports(rdb)

	// A workbook's first bytes are a zip signature, which is exactly the sort of
	// value a store that thought it held strings would damage.
	body := append([]byte("PK"), bytes.Repeat([]byte{0x00, 0xff}, 512)...)
	p, err := store.Keep(ctx, owner, FormatXLSX, &Export{
		Bytes:       body,
		ContentType: ContentType(FormatXLSX),
		Filename:    "बार्शी claims.xlsx",
		Rows:        7,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rdb.Del(ctx, docKey(owner, p.ID), indexKey(owner)).Err()
	})

	got, err := store.Fetch(ctx, owner, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Body, body) {
		t.Error("the document's bytes did not survive the round trip")
	}
	if got.Filename != "बार्शी claims.xlsx" || got.Rows != 7 {
		t.Errorf("the header came back as %+v", got.Prepared)
	}

	if _, err := store.Fetch(ctx, owner+"-someone-else", p.ID); !errors.Is(
		err, ErrNoPreparedExport) {
		t.Errorf("another officer's key resolved: %v", err)
	}

	// And the key is going to go on its own, which is the whole reason this is
	// in Redis rather than in a table somebody has to sweep.
	ttl, err := rdb.TTL(ctx, docKey(owner, p.ID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > PreparedTTL {
		t.Errorf("the document is held for %s, want up to %s", ttl, PreparedTTL)
	}
}

// TestTheBudgetHoldsTheDocumentsThisCanActuallyProduce checks the arithmetic.
//
// The per-officer budget was set to eight mebibytes on an estimate of what a
// thousand-row PDF weighs, and the estimate was four times the truth. An estimate
// in a comment is not checkable; this is, so the next person to move the row cap or
// add a format finds out here rather than in a Redis that signs officers out.
func TestTheBudgetHoldsTheDocumentsThisCanActuallyProduce(t *testing.T) {
	need := MaxPreparedPerOfficer * measuredLargestDocumentBytes
	if MaxPreparedBytesPerOfficer < need {
		t.Fatalf(
			"the budget is %d bytes, but %d documents of the largest measured "+
				"size (%d) need %d. Either the budget is too small to hold the "+
				"conversation this feature is for, or the measurement is stale -- "+
				"remeasure before raising it.",
			MaxPreparedBytesPerOfficer, MaxPreparedPerOfficer,
			measuredLargestDocumentBytes, need)
	}

	// And not wastefully large either, because every mebibyte here is a mebibyte
	// of a Redis that runs noeviction and is sized for sessions. Twice the worst
	// case is headroom; four times is how the eight came to be there.
	if MaxPreparedBytesPerOfficer > 2*need {
		t.Errorf(
			"the budget is %d bytes for a worst case of %d. That is more than "+
				"twice what can be stored under it, in a Redis where a full "+
				"instance is an error on the sign-in path.",
			MaxPreparedBytesPerOfficer, need)
	}
}

// TestTheMeasuredFiguresStillHoldAgainstARealConverter remeasures.
//
// Skipped unless a Gotenberg is configured, because the stub converter in the other
// tests returns a few bytes and a PDF measured against it is not a measurement. Run
// it with ESURVEY_TEST_PDF_ADDR pointing at one -- a throwaway container will do --
// whenever the row cap moves or the page template changes.
func TestTheMeasuredFiguresStillHoldAgainstARealConverter(t *testing.T) {
	addr := os.Getenv("ESURVEY_TEST_PDF_ADDR")
	if addr == "" {
		t.Skip("set ESURVEY_TEST_PDF_ADDR to remeasure against a real converter")
	}
	t.Skipf("remeasure by hand against %s; this is a reminder, not an assertion",
		addr)
}
