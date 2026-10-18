package gate

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// sharedRedis is the test Redis, database 14, flushed of gate keys.
func sharedRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("ESURVEY_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set ESURVEY_TEST_REDIS_ADDR to run the shared gate tests")
	}
	c := redis.NewClient(&redis.Options{
		Addr: addr, Password: os.Getenv("ESURVEY_TEST_REDIS_PASSWORD"), DB: 14,
	})
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Skipf("no redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// peak runs calls through gates and reports the most that ran at once.
func peak(t *testing.T, gates []*Gate, calls int) int64 {
	t.Helper()
	var now, most atomic.Int64
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			release, err := gates[i%len(gates)].Acquire(ctx)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			n := now.Add(1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			now.Add(-1)
		}()
	}
	wg.Wait()
	return most.Load()
}

func TestTwoInstancesTogetherStayInsideOneQuota(t *testing.T) {
	// The failure this exists for: four workers per instance, two instances,
	// eight calls against a project that admits four.
	rdb := sharedRedis(t)
	name := "test-quota-" + newToken()
	a := New(name, 4, rdb, nil)
	b := New(name, 4, rdb, nil)

	if got := peak(t, []*Gate{a, b}, 40); got > 4 {
		t.Fatalf("%d calls ran at once across two instances; the quota is 4", got)
	} else if got < 2 {
		t.Errorf("only %d ran at once; the gate is admitting less than it should", got)
	}
	if s := a.Stats(context.Background()); s.InUse != 0 || !s.Shared {
		t.Errorf("after every call released: %+v", s)
	}
}

func TestASlotAbandonedByADeadInstanceComesBack(t *testing.T) {
	rdb := sharedRedis(t)
	name := "test-dead-" + newToken()
	g := New(name, 1, rdb, nil)

	// A holder that vanished: its member is there, its lease already over.
	ctx := context.Background()
	if err := rdb.ZAdd(ctx, g.key(), redis.Z{
		Score: float64(time.Now().Add(-time.Second).UnixMilli()), Member: "dead",
	}).Err(); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	release, err := g.Acquire(cctx)
	if err != nil {
		t.Fatalf("the dead holder's slot was never reclaimed: %v", err)
	}
	release()
}

func TestWithoutTheSharedStoreThisInstanceIsStillBounded(t *testing.T) {
	// Redis gone. The gate must neither refuse everything nor admit
	// everything.
	dead := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond})
	defer dead.Close()
	g := New("test-down", 3, dead, nil)

	if got := peak(t, []*Gate{g}, 20); got > 3 || got < 1 {
		t.Fatalf("%d ran at once with the shared store down; want 1..3", got)
	}
	if g.Stats(context.Background()).Shared {
		t.Error("stats claim the count is shared while Redis is down")
	}
}

func TestAWaitEndsWithTheContext(t *testing.T) {
	g := New("test-cancel", 1, nil, nil)
	release, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.Acquire(ctx); err == nil {
		t.Fatal("acquired a slot that was held")
	}
}
