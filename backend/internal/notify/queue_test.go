package notify

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The outbox against a real database.
//
// These are the mechanics that cannot be checked any other way: that a
// message written inside a transaction is rolled back with it, that two
// dispatchers never claim the same row, and that a failure backs off instead
// of spinning. All three are properties of the SQL, not of the Go.

func testQueue(t *testing.T, sender Sender) (*Queue, *pgxpool.Pool) {
	t.Helper()

	url := os.Getenv("ESURVEY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set ESURVEY_TEST_DATABASE_URL to run the outbox tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// A small pool on purpose. pgxpool defaults MaxConns to the core count --
	// sixteen here -- and `go test ./...` runs these packages in parallel
	// against one Postgres that also has the running API attached. Four pools
	// of sixteen exhausts max_connections, and the symptom is not an error
	// but Begin blocking until the package times out. A test needs two.
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

	// Every test starts from an empty queue, and leaves it empty. This table
	// is not shared with anything else, so truncating it is safe — unlike the
	// report tables, which carry the fixtures other suites rely on.
	clear := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM notifications`)
	}
	clear()
	t.Cleanup(clear)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))
	return NewQueue(pool, sender, log), pool
}

func msg(ref string) Message {
	return Message{
		EventCode:  EventReportAcknowledged,
		FarmerID:   "F-test",
		Recipient:  "+919999900001",
		Body:       "तुमची पीक नुकसान सूचना " + ref + " मिळाली आहे.",
		ResourceID: ref,
	}
}

// --- a stub gateway ---------------------------------------------------------

type stubSender struct {
	mu   sync.Mutex
	sent []Message
	fail error
}

func (s *stubSender) Name() string { return "stub" }

func (s *stubSender) Send(_ context.Context, m Message) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail != nil {
		return "", s.fail
	}
	s.sent = append(s.sent, m)
	return "stub-ref-" + m.ResourceID, nil
}

func (s *stubSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

// --- the tests --------------------------------------------------------------

func TestAQueuedMessageIsDelivered(t *testing.T) {
	stub := &stubSender{}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	if err := q.Enqueue(ctx, pool, msg("CLR-1")); err != nil {
		t.Fatal(err)
	}

	sent, err := q.Dispatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 || stub.count() != 1 {
		t.Fatalf("sent = %d, gateway saw %d", sent, stub.count())
	}

	var state, ref string
	if err := pool.QueryRow(ctx,
		`SELECT state::text, COALESCE(provider_ref, '') FROM notifications`).
		Scan(&state, &ref); err != nil {
		t.Fatal(err)
	}
	if state != "sent" {
		t.Errorf("state = %q, want sent", state)
	}
	if ref == "" {
		// Without the provider's reference there is no way to chase a message
		// the farmer says never arrived.
		t.Error("the provider reference was not recorded")
	}
}

func TestARolledBackTransactionQueuesNothing(t *testing.T) {
	// The whole point of the outbox. If the acknowledgement does not commit,
	// the farmer must not be told it did.
	stub := &stubSender{}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, tx, msg("CLR-rollback")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d messages survived a rolled-back transaction", n)
	}
}

func TestACommittedTransactionQueuesExactlyOnce(t *testing.T) {
	stub := &stubSender{}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, tx, msg("CLR-commit")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("queued %d messages, want 1", n)
	}
}

func TestNoRecipientIsNotAnError(t *testing.T) {
	// Plenty of farmers have no number on record. Refusing the surrounding
	// transaction over it would block the acknowledgement itself.
	stub := &stubSender{}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	m := msg("CLR-nomobile")
	m.Recipient = ""
	if err := q.Enqueue(ctx, pool, m); err != nil {
		t.Fatalf("a missing number failed the caller: %v", err)
	}

	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM notifications`).Scan(&n)
	if n != 0 {
		t.Errorf("queued %d messages for a farmer with no number", n)
	}
}

func TestAnOptedOutFarmerIsSuppressedWithAReason(t *testing.T) {
	// Recorded as a decision rather than an absence: "we chose not to send
	// this" and "we failed to send this" are different facts.
	stub := &stubSender{}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO notification_preferences (farmer_id, opted_out_at, opted_out_reason)
		VALUES ('F-optout', now(), 'asked at the counter')
		ON CONFLICT (farmer_id) DO UPDATE SET opted_out_at = now()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM notification_preferences WHERE farmer_id = 'F-optout'`)
	})

	m := msg("CLR-optout")
	m.FarmerID = "F-optout"
	if err := q.Enqueue(ctx, pool, m); err != nil {
		t.Fatal(err)
	}

	var state, reason string
	if err := pool.QueryRow(ctx,
		`SELECT state::text, COALESCE(suppressed_reason, '') FROM notifications`).
		Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "suppressed" {
		t.Errorf("state = %q, want suppressed", state)
	}
	if reason == "" {
		t.Error("suppressed with no reason recorded")
	}

	// And it must not go out.
	if sent, _ := q.Dispatch(ctx, 10); sent != 0 {
		t.Errorf("sent %d messages to a farmer who opted out", sent)
	}
}

func TestAFailedSendBacksOffRatherThanSpinning(t *testing.T) {
	stub := &stubSender{fail: errors.New("gateway refused")}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	if err := q.Enqueue(ctx, pool, msg("CLR-fail")); err != nil {
		t.Fatal(err)
	}

	if sent, err := q.Dispatch(ctx, 10); err != nil || sent != 0 {
		t.Fatalf("sent = %d, err = %v", sent, err)
	}

	var state string
	var attempts int
	var due time.Time
	if err := pool.QueryRow(ctx,
		`SELECT state::text, attempts, next_attempt_at FROM notifications`).
		Scan(&state, &attempts, &due); err != nil {
		t.Fatal(err)
	}
	if state != "failed" {
		t.Errorf("state = %q, want failed", state)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	// The backoff lives in the column, not in a sleeping goroutine, so a
	// restart does not lose the schedule or retry everything at once.
	if !due.After(time.Now()) {
		t.Error("the next attempt is due immediately; there is no backoff")
	}

	// A second dispatch must not pick it up again straight away.
	if sent, _ := q.Dispatch(ctx, 10); sent != 0 {
		t.Error("a backed-off message was retried immediately")
	}
}

func TestARepeatedlyFailingMessageIsAbandoned(t *testing.T) {
	// A number that has refused five times over a day is wrong. Retrying it
	// forever costs money and tells nobody.
	stub := &stubSender{fail: errors.New("unreachable")}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	if err := q.Enqueue(ctx, pool, msg("CLR-abandon")); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < MaxAttempts; i++ {
		// The backoff would otherwise make this test take hours.
		if _, err := pool.Exec(ctx,
			`UPDATE notifications SET next_attempt_at = now() - interval '1 minute'`); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Dispatch(ctx, 10); err != nil {
			t.Fatal(err)
		}
	}

	var state string
	var attempts int
	if err := pool.QueryRow(ctx,
		`SELECT state::text, attempts FROM notifications`).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "abandoned" {
		t.Errorf("state = %q after %d attempts, want abandoned", state, attempts)
	}
}

func TestTwoDispatchersNeverClaimTheSameMessage(t *testing.T) {
	// FOR UPDATE SKIP LOCKED. Sending a farmer the same notice twice is a
	// small thing; doing it because the deployment scaled is not.
	stub := &stubSender{}
	q, pool := testQueue(t, stub)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		if err := q.Enqueue(ctx, pool, msg("CLR-race-"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	totals := make([]int, 4)
	for i := range totals {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			sent, err := q.Dispatch(context.Background(), 20)
			if err != nil {
				t.Error(err)
			}
			totals[n] = sent
		}(i)
	}
	wg.Wait()

	total := 0
	for _, n := range totals {
		total += n
	}
	if total != 20 {
		t.Errorf("delivered %d of 20 — a message was sent twice or lost", total)
	}
	if stub.count() != 20 {
		t.Errorf("the gateway saw %d sends for 20 messages", stub.count())
	}
}

func TestBacklogReportsWhatIsWaiting(t *testing.T) {
	// What /readyz shows. A platform that has queued messages and delivered
	// none should say so on its own status page.
	q, pool := testQueue(t, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := q.Enqueue(ctx, pool, msg("CLR-backlog")); err != nil {
			t.Fatal(err)
		}
	}

	b, err := q.Backlog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.Queued != 3 {
		t.Errorf("queued = %d, want 3", b.Queued)
	}
	if b.Provider != "none" {
		t.Errorf("provider = %q, want none", b.Provider)
	}
	if b.Oldest == "" {
		// The age of the oldest is what turns a count into a problem.
		t.Error("the oldest queued time was not reported")
	}
}

func TestDispatchWithNoProviderIsItsOwnError(t *testing.T) {
	// Distinct from a delivery failure: the message is fine, the platform has
	// no way to send it, and treating that as a failed attempt would burn the
	// retry budget on a problem no retry fixes.
	q, pool := testQueue(t, nil)
	ctx := context.Background()

	if err := q.Enqueue(ctx, pool, msg("CLR-noprovider")); err != nil {
		t.Fatal(err)
	}

	_, err := q.Dispatch(ctx, 10)
	if !errors.Is(err, ErrNoSender) {
		t.Fatalf("err = %v, want ErrNoSender", err)
	}

	var attempts int
	_ = pool.QueryRow(ctx, `SELECT attempts FROM notifications`).Scan(&attempts)
	if attempts != 0 {
		t.Errorf("attempts = %d; a missing provider consumed the retry budget", attempts)
	}
}

func TestRunStopsImmediatelyWithNoProvider(t *testing.T) {
	// Quiet rather than spinning. A worker logging "no provider" every
	// interval trains everyone to ignore the log that will eventually carry a
	// real failure.
	q, _ := testQueue(t, nil)

	done := make(chan struct{})
	go func() {
		q.Run(context.Background(), time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("Run did not return with no provider configured")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	q, _ := testQueue(t, &stubSender{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.Run(ctx, 10*time.Millisecond)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("Run ignored a cancelled context")
	}
}

func TestEveryEventCodeRendersForTheOutbox(t *testing.T) {
	// A message queued with an empty body is rejected by Enqueue, so a code
	// whose template is missing would fail at the moment it mattered.
	for _, code := range []string{
		EventReportAcknowledged, EventReportVerified, EventReportAdjudicated,
		EventGrievanceFiled, EventGrievanceDecided,
	} {
		body, err := Render(code, Vars{
			Reference: "CLR-2025-000041",
			Village:   "मालेगाव",
			Figure:    "६२%",
			Outcome:   "हरकत मान्य झाली",
		})
		if err != nil || strings.TrimSpace(body) == "" {
			t.Errorf("%s: %v", code, err)
		}
	}
}
