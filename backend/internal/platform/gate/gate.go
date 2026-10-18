// Package gate bounds how many calls every API instance together makes to one
// upstream at a time.
//
// # Why a gate and not a worker count
//
// The assessment workers used to be the limit: four per instance, "to match the
// satellite service's Earth Engine concurrency". That tied three different
// quotas -- the analytics engine's, the radar service's, the model provider's
// -- to one number, and multiplied it by however many instances were running.
// Two instances was eight calls against a project that admits four, and the
// excess came back as 429s that burnt retry attempts; raising the worker count
// to use a larger model quota would have overrun the satellite one.
//
// So each upstream has its own gate, sized to its own quota, counted across the
// whole deployment. Workers can be many -- one waiting at a gate costs a
// goroutine -- and each upstream sees exactly as many calls as it admits.
//
// # How
//
// A Redis sorted set per upstream: one member per held slot, scored by when
// its lease runs out. Taking a slot drops expired members and adds one only
// while fewer than the limit remain, atomically in one script. A holder
// renews its lease while it works, so a slot is never taken from a call that
// is merely slow; a process that dies stops renewing, and its slot comes back
// within one lease.
//
// # When Redis is not there
//
// The gate falls back to a semaphore in this process, at the same limit. With
// more than one instance that admits more than the quota, and the upstream's
// 429 is retried; the alternative -- refusing every assessment while Redis is
// down -- stops claims for a cache outage. Logged once per outage so it is
// seen.
package gate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
)

// Lease is how long a slot is held without renewal before it is presumed
// abandoned. Renewed every third of it.
const Lease = 30 * time.Second

// recheck is how long the gate counts locally after the shared store failed
// before trying it again.
const recheck = 10 * time.Second

// take drops expired holders, then adds this one if there is room.
//
// KEYS[1] the set; ARGV: now (ms), expiry (ms), limit, token.
var take = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) < tonumber(ARGV[3]) then
  redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
  redis.call('PEXPIRE', KEYS[1], 600000)
  return 1
end
return 0`)

// renew extends a slot this holder still has. A slot already expired and
// reclaimed is not resurrected: XX only updates an existing member.
var renew = redis.NewScript(`
local n = redis.call('ZADD', KEYS[1], 'XX', 'CH', ARGV[1], ARGV[2])
redis.call('PEXPIRE', KEYS[1], 600000)
return n`)

// Gate is one upstream's limit.
type Gate struct {
	name  string
	limit int
	rdb   redis.UniversalClient
	log   *slog.Logger

	local chan struct{}

	// For the health page. Local to this instance except InUse, which is
	// read from Redis where it can be.
	waiting  atomic.Int64
	holding  atomic.Int64
	acquired atomic.Int64
	waitedNs atomic.Int64
	fallback atomic.Bool
	// downUntil is when to try the shared store again after it failed, in
	// Unix ms. Without it every call in an outage paid Redis's dial timeout
	// before falling back.
	downUntil atomic.Int64
	warnOnce  sync.Mutex
}

// New builds a gate. A nil Redis client means this process alone is counted.
// A limit below one is treated as one: a gate that admits nothing would stop
// every assessment silently.
func New(name string, limit int, rdb redis.UniversalClient, log *slog.Logger) *Gate {
	if limit < 1 {
		limit = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &Gate{
		name: name, limit: limit, rdb: rdb, log: log,
		local: make(chan struct{}, limit),
	}
}

// Name is the upstream this gate guards.
func (g *Gate) Name() string { return g.name }

// Limit is how many calls it admits at once.
func (g *Gate) Limit() int { return g.limit }

func (g *Gate) key() string { return "esurvey:gate:" + g.name }

// Acquire waits for a slot and returns the function that gives it back.
//
// Returns ctx.Err() if the context ends first. The release function is safe to
// call more than once.
func (g *Gate) Acquire(ctx context.Context) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	g.waiting.Add(1)
	began := time.Now()
	defer g.waiting.Add(-1)

	token := newToken()
	pause := 50 * time.Millisecond
	for {
		ok, err := g.tryShared(ctx, token)
		if err != nil && ctx.Err() != nil {
			return nil, busy(ctx)
		}
		if err != nil {
			// Redis is not answering: this process counts alone.
			g.noteFallback(err)
			return g.acquireLocal(ctx, began)
		}
		g.fallback.Store(false)
		if ok {
			return g.held(ctx, token, began), nil
		}

		t := time.NewTimer(pause + jitter(pause))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, busy(ctx)
		case <-t.C:
		}
		pause = min(pause*2, time.Second)
	}
}

func (g *Gate) tryShared(ctx context.Context, token string) (bool, error) {
	if g.rdb == nil {
		return false, errNoRedis
	}
	now := time.Now()
	if now.UnixMilli() < g.downUntil.Load() {
		return false, errNoRedis
	}
	n, err := take.Run(ctx, g.rdb, []string{g.key()},
		now.UnixMilli(), now.Add(Lease).UnixMilli(), g.limit, token).Int()
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		g.downUntil.Store(now.Add(recheck).UnixMilli())
		return false, err
	}
	return n == 1, nil
}

var errNoRedis = errors.New("no shared store")

// ErrBusy is returned, wrapping the context's error, when the context ended
// while still waiting for a slot: the call never started. Callers use it to
// tell "the upstream is full" from "the upstream failed".
var ErrBusy = errors.New("every slot for this upstream stayed taken")

func busy(ctx context.Context) error {
	return fmt.Errorf("%w: %w", ErrBusy, ctx.Err())
}

// held starts the renewal and returns the release.
func (g *Gate) held(ctx context.Context, token string, began time.Time) func() {
	g.count(began)

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(Lease / 3)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// Detached from the caller's context: a call cancelled
				// mid-flight still holds its slot until it releases.
				rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = renew.Run(rctx, g.rdb, []string{g.key()},
					time.Now().Add(Lease).UnixMilli(), token).Result()
				cancel()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(stop)
			g.holding.Add(-1)
			rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			// If this fails the lease runs out on its own.
			_ = g.rdb.ZRem(rctx, g.key(), token).Err()
		})
	}
}

func (g *Gate) acquireLocal(ctx context.Context, began time.Time) (func(), error) {
	select {
	case <-ctx.Done():
		return nil, busy(ctx)
	case g.local <- struct{}{}:
	}
	g.count(began)
	var once sync.Once
	return func() {
		once.Do(func() {
			g.holding.Add(-1)
			<-g.local
		})
	}, nil
}

func (g *Gate) count(began time.Time) {
	g.holding.Add(1)
	g.acquired.Add(1)
	g.waitedNs.Add(int64(time.Since(began)))
}

func (g *Gate) noteFallback(err error) {
	if g.fallback.Swap(true) {
		return
	}
	if g.rdb == nil {
		return
	}
	g.warnOnce.Lock()
	defer g.warnOnce.Unlock()
	g.log.Warn("upstream gate counting this instance only; the shared store is unavailable",
		"gate", g.name, "limit", g.limit, "error", err)
}

// Stats is a gate's state, for the health page.
type Stats struct {
	Name  string `json:"name"`
	Limit int    `json:"limit"`
	// InUse is slots held across every instance, or -1 where the shared
	// store could not be read.
	InUse int64 `json:"in_use"`
	// HeldHere and WaitingHere are this instance's.
	HeldHere    int64 `json:"held_here"`
	WaitingHere int64 `json:"waiting_here"`
	// AvgWaitMS is the mean wait for a slot in this instance since start.
	AvgWaitMS float64 `json:"avg_wait_ms"`
	Acquired  int64   `json:"acquired"`
	// Shared is false while the gate is counting this instance only.
	Shared bool `json:"shared"`
}

// Stats reads the gate.
func (g *Gate) Stats(ctx context.Context) Stats {
	s := Stats{
		Name: g.name, Limit: g.limit, InUse: -1,
		HeldHere: g.holding.Load(), WaitingHere: g.waiting.Load(),
		Acquired: g.acquired.Load(),
		Shared:   g.rdb != nil && !g.fallback.Load(),
	}
	if n := s.Acquired; n > 0 {
		s.AvgWaitMS = float64(g.waitedNs.Load()) / float64(n) / 1e6
	}
	if g.rdb != nil {
		rctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		now := time.Now().UnixMilli()
		if n, err := g.rdb.ZCount(rctx, g.key(), "("+itoa(now), "+inf").Result(); err == nil {
			s.InUse = n
		}
	}
	return s
}

func itoa(n int64) string {
	return big.NewInt(n).String()
}

func newToken() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// jitter is a random wait in [0, max), from crypto/rand, for the same reason
// report.jitter is: this codebase does not import math/rand.
func jitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return time.Duration(n.Int64())
}
