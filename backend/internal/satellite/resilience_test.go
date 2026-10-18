package satellite

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// This upstream is called before the assessment panel, so everything the
// panel does to degrade gracefully is unreachable if the call itself fails.
// A dropped connection here is a failed assessment.

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c := New(srv.URL, "test-key")
	// The real backoff would make this suite take seconds to prove something
	// that is not about the duration.
	c.HTTP = &http.Client{Timeout: 5 * time.Second}
	return c
}

func TestATransientFailureIsRetried(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	if _, err := c.do(context.Background(), http.MethodGet, "/x", nil); err != nil {
		t.Fatalf("a retryable failure was not retried: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("calls = %d, want 2", n)
	}
}

func TestARejectedRequestIsNotRetried(t *testing.T) {
	// A 422 for a malformed polygon will be a 422 every time. Retrying it
	// spends an officer's time to reach the same answer.
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"LATLON_SWAPPED","message":"bad ring"}}`))
	})

	_, err := c.do(context.Background(), http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("a 422 was reported as success")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1 — a client error was retried", n)
	}

	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.Code != "LATLON_SWAPPED" {
		t.Errorf("the engine's own reason was lost: %v", err)
	}
}

func TestAnAcceptedSubmitIsNotRetried(t *testing.T) {
	// The engine answered, so it has the task. Retrying would start a second
	// billable analysis of the same parcel and leave two task ids for one
	// assessment.
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	_, _ = c.do(context.Background(), http.MethodPost, "/submit", map[string]string{"a": "b"})
	if n := calls.Load(); n != 1 {
		t.Errorf("calls = %d, want 1 — a submit the server received was retried", n)
	}
}

func TestRetryableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nothing failed", nil, false},
		{"server error", &Error{HTTPStatus: 503}, true},
		{"rate limited", &Error{HTTPStatus: 429}, true},
		{"bad request", &Error{HTTPStatus: 400}, false},
		{"unauthorised", &Error{HTTPStatus: 401}, false},
		{"not found", &Error{HTTPStatus: 404}, false},
		{"unprocessable", &Error{HTTPStatus: 422}, false},

		// The caller gave up. Retrying would ignore a cancellation.
		{"cancelled", context.Canceled, false},
		{"deadline passed", context.DeadlineExceeded, false},

		{"connection refused", &net.OpError{Op: "dial", Err: errors.New("refused")}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := retryable(c.err); got != c.want {
				t.Errorf("retryable = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSentToServer(t *testing.T) {
	// Governs whether a submit may be retried.
	if sentToServer(&net.OpError{Op: "dial", Err: errors.New("refused")}) {
		t.Error("a refused connection was treated as delivered")
	}
	if !sentToServer(&Error{HTTPStatus: 500}) {
		t.Error("a server that answered was treated as never reached")
	}
	// A reset mid-flight may have been acted on, so it counts as delivered.
	if !sentToServer(&net.OpError{Op: "read", Err: errors.New("reset")}) {
		t.Error("a mid-flight failure was assumed not to have arrived")
	}
}

// --- the breaker ------------------------------------------------------------

func TestBreakerOpensAfterRepeatedFailures(t *testing.T) {
	var b breaker
	now := time.Now()

	for i := 0; i < breakerThreshold-1; i++ {
		b.failed(now)
		if !b.allow(now) {
			t.Fatalf("the circuit opened after %d failures, before the threshold", i+1)
		}
	}

	b.failed(now)
	if b.allow(now) {
		t.Error("the circuit stayed closed at the threshold")
	}

	open, failures := b.state()
	if !open || failures != breakerThreshold {
		t.Errorf("state = open %v, failures %d", open, failures)
	}
}

func TestBreakerProbesAfterCooldown(t *testing.T) {
	// A recovered engine must be picked up without a restart.
	var b breaker
	now := time.Now()
	for i := 0; i < breakerThreshold; i++ {
		b.failed(now)
	}
	if b.allow(now) {
		t.Fatal("the circuit did not open")
	}

	later := now.Add(breakerCooldown + time.Second)
	if !b.allow(later) {
		t.Error("no probe was allowed after the cooldown")
	}
	// One probe only: a second immediate call must still be refused, or a
	// burst of requests would all hit a dead upstream.
	if b.allow(later) {
		t.Error("the cooldown restarted but a second call was let through")
	}
}

func TestBreakerClosesOnSuccess(t *testing.T) {
	var b breaker
	now := time.Now()
	for i := 0; i < breakerThreshold-1; i++ {
		b.failed(now)
	}
	b.succeeded()

	if open, failures := b.state(); open || failures != 0 {
		t.Errorf("a success did not reset the count: open %v, failures %d", open, failures)
	}
}

func TestAnOpenCircuitFailsFastWithItsOwnError(t *testing.T) {
	// The point of the breaker: an officer learns the service is down in
	// milliseconds rather than waiting out the full timeout, and the message
	// says so rather than surfacing a connection error that reads as their
	// mistake.
	var calls atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	for i := 0; i < breakerThreshold; i++ {
		_, _ = c.do(context.Background(), http.MethodPost, "/submit", map[string]string{})
	}
	before := calls.Load()

	_, err := c.do(context.Background(), http.MethodGet, "/x", nil)
	if !errors.Is(err, ErrUpstreamDown) {
		t.Fatalf("err = %v, want ErrUpstreamDown", err)
	}
	if calls.Load() != before {
		t.Error("a call was made while the circuit was open")
	}
}

func TestARejectedRequestDoesNotOpenTheCircuit(t *testing.T) {
	// The engine is working and said no. A run of malformed polygons must not
	// take the upstream out for everyone else.
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"BAD_GEOMETRY","message":"nope"}}`))
	})

	for i := 0; i < breakerThreshold+2; i++ {
		_, _ = c.do(context.Background(), http.MethodGet, "/x", nil)
	}

	if open, failures := c.Health(); open || failures != 0 {
		t.Errorf("client errors opened the circuit: open %v, failures %d", open, failures)
	}
}

func TestBackoffRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backoff(ctx, 3); !errors.Is(err, context.Canceled) {
		t.Errorf("backoff ignored a cancelled context: %v", err)
	}
}
