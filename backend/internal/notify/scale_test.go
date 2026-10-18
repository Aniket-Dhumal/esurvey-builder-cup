package notify

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// A gateway that answers as fast as a real one: an HTTPS round trip to an
// aggregator, which is tens to hundreds of milliseconds.
type slowSender struct {
	latency time.Duration
	sent    atomic.Int64
}

func (s *slowSender) Name() string { return "slow-stub" }

func (s *slowSender) Send(ctx context.Context, m Message) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(s.latency):
	}
	s.sent.Add(1)
	return "ref-" + m.ResourceID, nil
}

// How fast the outbox can actually reach farmers.
//
// A district-wide event acknowledges every claim filed and, weeks later,
// tells every farmer their decision. At lakhs of claims that is lakhs of
// messages, and the queue drains them one at a time. Set ESURVEY_SCALE=1.
func TestOutboxDispatchRate(t *testing.T) {
	if os.Getenv("ESURVEY_SCALE") == "" {
		t.Skip("set ESURVEY_SCALE=1 to measure outbox throughput")
	}
	sender := &slowSender{latency: 80 * time.Millisecond}
	q, _ := testQueue(t, sender)
	ctx := context.Background()

	const queued = 200
	for i := 0; i < queued; i++ {
		if err := q.Enqueue(ctx, nil, msg(fmt.Sprintf("CLR-%05d", i))); err != nil {
			t.Fatal(err)
		}
	}

	began := time.Now()
	sent, err := q.Dispatch(ctx, 200)
	took := time.Since(began)
	if err != nil {
		t.Fatal(err)
	}

	perSecond := float64(sent) / took.Seconds()
	t.Logf("%d messages in %s -> %.0f/s (%.0f/hour) with an %s gateway",
		sent, took.Round(time.Millisecond), perSecond, perSecond*3600,
		sender.latency)

	// One lakh of acknowledgements is what one district-wide event produces.
	t.Logf("a lakh of messages would take %s at this rate",
		(time.Duration(float64(100000)/perSecond) * time.Second).Round(time.Minute))

	if perSecond < 50 {
		t.Errorf("the outbox sends %.0f/s; a district's messages would take days",
			perSecond)
	}
}
