package db

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Router chooses where a staleness-tolerant read goes: the replica while it is
// answering and close behind, the primary otherwise.
//
// # Which reads
//
// Only those that are fine a few seconds old and are not read back by the
// person who just wrote them: the state-wide view, the overview dashboard, the
// audit search, the registry listings, the product administrator's counts.
// Sign-in, permissions, a claim an officer has just approved, a farmer's own
// progress -- all stay on the primary, because a replica a second behind would
// show an officer the claim they approved as still waiting, or keep a revoked
// posting alive for a second.
//
// # Falling back
//
// The replica is checked every few seconds. If it does not answer, or its lag
// is over the limit, reads go to the primary until it recovers. Replica lag
// after a burst of writes is normal; a dashboard that silently shows figures
// from ten minutes ago is not.
type Router struct {
	primary *Pool
	replica *Pool
	maxLag  time.Duration
	log     *slog.Logger

	useReplica atomic.Bool
	lagMs      atomic.Int64
	// behind counts consecutive checks over the lag limit.
	behind int

	mu      sync.Mutex
	reason  string
	checked time.Time
}

// NewRouter builds a router. A nil replica reads from the primary always.
func NewRouter(primary, replica *Pool, maxLag time.Duration, log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	if maxLag <= 0 {
		maxLag = 10 * time.Second
	}
	r := &Router{primary: primary, replica: replica, maxLag: maxLag, log: log}
	if replica == nil {
		r.reason = "no replica configured"
	}
	return r
}

// Read is the pool for a staleness-tolerant read.
func (r *Router) Read() *Pool {
	if r == nil {
		return nil
	}
	if r.replica != nil && r.useReplica.Load() {
		return r.replica
	}
	return r.primary
}

// Primary is the primary pool, for callers that need both.
func (r *Router) Primary() *Pool { return r.primary }

// lagQuery is how far behind the replica is.
//
// A replica that has replayed everything it received is not behind, however
// long ago the last transaction was: replay timestamps stop moving when the
// primary is idle, and reading them alone reports a quiet night as lag.
const lagQuery = `
	SELECT CASE
	  WHEN NOT pg_is_in_recovery() THEN 0
	  WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0
	  ELSE COALESCE(extract(epoch FROM now() - pg_last_xact_replay_timestamp()), 0)
	END * 1000`

// Check measures the replica once and routes accordingly.
func (r *Router) Check(ctx context.Context) {
	if r == nil || r.replica == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	var lag float64
	err := r.replica.QueryRow(cctx, lagQuery).Scan(&lag)

	var use bool
	var reason string
	r.mu.Lock()
	switch {
	case err != nil:
		r.behind = 0
		reason = "the replica is not answering: " + err.Error()
	case time.Duration(lag)*time.Millisecond > r.maxLag:
		// Twice in a row before moving. After a quiet spell the first write
		// to arrive reads as the whole quiet spell of lag until it is
		// replayed a moment later -- replay timestamps only move with
		// transactions -- and one such reading is not a replica falling
		// behind. Measured: an idle replica read 22s behind on the first
		// write after 22s of nothing.
		r.behind++
		reason = "the replica is " + (time.Duration(lag) * time.Millisecond).Round(time.Second).String() +
			" behind, over the " + r.maxLag.String() + " limit"
		use = r.behind < 2 && r.useReplica.Load()
	default:
		r.behind = 0
		use = true
	}
	r.mu.Unlock()
	r.lagMs.Store(int64(lag))

	was := r.useReplica.Swap(use)
	r.mu.Lock()
	r.reason = reason
	r.checked = time.Now()
	r.mu.Unlock()

	if was != use {
		if use {
			r.log.Info("reads back on the replica", "lag_ms", int64(lag))
		} else {
			r.log.Warn("reads moved to the primary", "reason", reason)
		}
	}
}

// Watch checks the replica until the context ends.
func (r *Router) Watch(ctx context.Context, every time.Duration) {
	if r == nil || r.replica == nil {
		return
	}
	r.Check(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.Check(ctx)
		}
	}
}

// RouterStatus is the router's state, for the health page.
type RouterStatus struct {
	ReplicaConfigured bool      `json:"replica_configured"`
	ReadingFrom       string    `json:"reading_from"`
	LagMS             int64     `json:"lag_ms"`
	MaxLagMS          int64     `json:"max_lag_ms"`
	Reason            string    `json:"reason,omitempty"`
	CheckedAt         time.Time `json:"checked_at,omitzero"`
}

// Status reads the router.
func (r *Router) Status() RouterStatus {
	s := RouterStatus{ReadingFrom: "primary"}
	if r == nil {
		return s
	}
	s.ReplicaConfigured = r.replica != nil
	s.MaxLagMS = r.maxLag.Milliseconds()
	s.LagMS = r.lagMs.Load()
	if r.replica != nil && r.useReplica.Load() {
		s.ReadingFrom = "replica"
	}
	r.mu.Lock()
	s.Reason, s.CheckedAt = r.reason, r.checked
	r.mu.Unlock()
	return s
}
