package satellite

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

// Retry and a circuit breaker for the analytics engine.
//
// # Why this upstream needs both
//
// The engine is a single deployment on a fixed address, reached over the
// public internet, and it is called *before* the assessment panel. Everything
// downstream degrades carefully -- a specialist that fails is a recorded gap,
// a panel that cannot run falls back to a deterministic assessment -- and all
// of that is unreachable if this call fails, because the request never gets
// that far. A dropped connection here is a failed assessment.
//
// So: transient failures are retried, and a genuinely dead upstream is
// reported quickly rather than making every officer wait out a 60-second
// timeout to be told the same thing.

const (
	// maxAttempts is the original call plus two retries. Enough for a dropped
	// connection or a restarting server; not enough to keep an officer
	// waiting while something properly broken is retried into the ground.
	maxAttempts = 3

	// retryBase is the first backoff, doubling after that. The engine's work
	// is measured in seconds, so a sub-second retry would arrive before it
	// had finished failing.
	retryBase = 500 * time.Millisecond

	// breakerThreshold is how many consecutive failures open the circuit.
	breakerThreshold = 5

	// breakerCooldown is how long it stays open. Long enough for a restart to
	// finish, short enough that a recovered engine is picked up within the
	// time it takes an officer to try again.
	breakerCooldown = 30 * time.Second
)

// ErrUpstreamDown is returned while the circuit is open.
//
// Its own error so the API layer can say "the analysis service is
// unavailable, try shortly" rather than surfacing a connection error, which
// reads to an officer as though they did something wrong.
var ErrUpstreamDown = errors.New("the satellite analysis service is not responding")

// breaker tracks consecutive failures against one upstream.
type breaker struct {
	mu       sync.Mutex
	failures int
	openedAt time.Time
}

// allow reports whether a call may proceed.
func (b *breaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.failures < breakerThreshold {
		return true
	}
	if now.Sub(b.openedAt) >= breakerCooldown {
		// Half-open: let one call through to find out. Its result settles
		// whether the circuit closes or the cooldown restarts.
		b.openedAt = now
		return true
	}
	return false
}

func (b *breaker) succeeded() {
	b.mu.Lock()
	b.failures = 0
	b.mu.Unlock()
}

func (b *breaker) failed(now time.Time) {
	b.mu.Lock()
	b.failures++
	if b.failures == breakerThreshold {
		b.openedAt = now
	}
	b.mu.Unlock()
}

// state is for the readiness endpoint: an operator should be able to see that
// this upstream is the reason assessments are failing.
func (b *breaker) state() (open bool, failures int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures >= breakerThreshold, b.failures
}

// retryable reports whether an error is worth trying again.
//
// The distinction is not "did it fail" but "might the same request succeed".
// A 422 for a malformed polygon will be a 422 every time, and retrying it
// wastes an officer's time to reach the same answer.
func retryable(err error) bool {
	if err == nil {
		return false
	}

	// A context that is done is the caller giving up, not the upstream
	// failing. Retrying would ignore a cancellation.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	var apiErr *Error
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.HTTPStatus == http.StatusTooManyRequests:
			return true
		case apiErr.HTTPStatus >= 500:
			return true
		default:
			// Every other 4xx is about the request, and the request will not
			// have changed by the time it is sent again.
			return false
		}
	}

	// Anything else that reached here is a transport failure: the connection
	// was refused, reset, or timed out.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return true
}

// sentToServer reports whether a failure means the request may already have
// been acted on.
//
// This governs whether a *submit* may be retried. A dropped connection after
// the engine accepted the task would, on retry, start a second analysis of
// the same parcel -- billable work, and two task ids for one assessment. So a
// submit is retried only where the request demonstrably never arrived, or
// where the server itself said to try again.
func sentToServer(err error) bool {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		// The server answered, so it received the request.
		return true
	}

	// A refused connection never reached a listener. Anything else --
	// a reset mid-flight, a timeout waiting for the response -- may have.
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return false
	}
	return true
}

// backoff is how long to wait before attempt n (1-indexed), with the context
// deciding whether the wait completes at all.
func backoff(ctx context.Context, attempt int) error {
	d := retryBase << (attempt - 1)
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
