package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/config"
)

// The API's connection pools, one per kind of work.
//
// # Why more than one
//
// One pool of twenty served farmers' requests, the claim pipeline, twenty-four
// assessment workers, the SMS outbox and the hourly sweeps. After a hailstorm
// the background work is exactly what grows, and it took the connections a
// farmer filing a claim was waiting for: the request timed out in the pool
// while the database had capacity to spare. So the work that can wait has its
// own pool, bounded, and cannot starve the work that cannot.
//
// # Why a direct pool
//
// Behind a transaction pooler (PgBouncer) a connection is shared between
// clients one transaction at a time, so nothing set on the session survives:
// not the migrator's advisory lock, and not the audit chain's seed, which the
// trigger reads with current_setting(). Those two use a small pool straight to
// the primary. The seed is never put on a pooled connection -- or in the role's
// settings, where anyone who can read the catalog could read it.

// Role is what a pool is for.
type Role string

const (
	// Requests serves API requests: writes, and reads that must see them.
	Requests Role = "api"
	// Workers serves the background loops.
	Workers Role = "worker"
	// Reads serves staleness-tolerant reads, from a replica where configured.
	Reads Role = "read"
	// Direct reaches the primary unpooled: migrations and the audit log.
	Direct Role = "direct"
)

// directMaxConns is the direct pool's size: the audit recorder's flushes and a
// migration. It is kept small because behind a pooler these are the only
// connections that count against the primary one-for-one.
const directMaxConns = 4

// OpenFor builds the pool for a role. Reads with no replica configured returns
// nil; the Router then reads from the primary.
func OpenFor(ctx context.Context, cfg config.Database, role Role) (*Pool, error) {
	c := cfg
	pooled := cfg.Pooled
	switch role {
	case Requests:
	case Workers:
		c.MaxConns = cfg.WorkerMaxConns
		c.MinConns = min(cfg.MinConns, cfg.WorkerMaxConns)
	case Reads:
		if cfg.ReadURL == "" {
			return nil, nil
		}
		c.URL = cfg.ReadURL
		c.MaxConns = cfg.ReadMaxConns
		c.MinConns = min(cfg.MinConns, cfg.ReadMaxConns)
		// A replica cannot hold the audit chain's inserts anyway.
		c.AuditChainSeed = ""
	case Direct:
		if cfg.DirectURL != "" {
			c.URL = cfg.DirectURL
		}
		c.MaxConns = directMaxConns
		c.MinConns = 1
		pooled = false
	default:
		return nil, fmt.Errorf("unknown pool role %q", role)
	}
	return open(ctx, c, "esurvey-"+string(role), pooled)
}

// Open builds the requests pool. Kept for callers that need only one.
func Open(ctx context.Context, cfg config.Database) (*Pool, error) {
	return open(ctx, cfg, "esurvey-api", cfg.Pooled)
}

func open(ctx context.Context, cfg config.Database, name string, pooled bool) (*Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parsing database url for %s: %w", name, err)
	}

	pc.MaxConns = cfg.MaxConns
	pc.MinConns = cfg.MinConns
	pc.MaxConnLifetime = cfg.MaxConnLifetime
	pc.MaxConnIdleTime = cfg.MaxConnIdleTime
	// Jitter stops a pool built at boot from recycling every connection at
	// the same instant an hour later.
	pc.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 10

	if pc.ConnConfig.RuntimeParams == nil {
		pc.ConnConfig.RuntimeParams = map[string]string{}
	}
	// application_name is one of the few parameters a pooler carries, and
	// it is what tells the four pools apart in pg_stat_activity.
	pc.ConnConfig.RuntimeParams["application_name"] = name

	if !pooled {
		// A statement timeout set on the connection binds every query on it,
		// including ones written later by someone who did not think about it.
		// An unbounded query on a shared pool is how one slow report takes
		// down an API. Behind a pooler the same values are role defaults; see
		// EnsureRoleDefaults.
		pc.ConnConfig.RuntimeParams["statement_timeout"] =
			fmt.Sprintf("%d", cfg.StatementTimeout.Milliseconds())
		// Nothing here holds a transaction open across a network call; a
		// stuck one is a bug, and it should surface rather than pin a
		// connection.
		pc.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"

		// The audit chain's seed, as a session setting the trigger reads.
		//
		// A RuntimeParam rather than a SET on each checkout, so it is
		// established as part of the connection handshake and cannot be
		// missing on a connection that was recycled mid-request. Custom
		// parameters need the namespace prefix; Postgres rejects an
		// unqualified one.
		//
		// Empty is legal and is what a development stack runs with. The
		// trigger treats the absent setting as an empty seed, so the chain
		// still links -- see migration 0020 for what that does and does not
		// protect.
		if cfg.AuditChainSeed != "" {
			pc.ConnConfig.RuntimeParams["esurvey.audit_seed"] = cfg.AuditChainSeed
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("creating the %s pool: %w", name, err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reaching the database for %s: %w", name, err)
	}
	return pool, nil
}

// EnsureRoleDefaults sets the statement and idle-transaction timeouts as
// defaults on the connecting role.
//
// For a pooled deployment, where they cannot travel on the connection. They
// take effect on connections the pooler opens after this runs, which is why it
// runs at startup before the pooled pools are built. Harmless where not
// pooled: the connection's own settings override a role default.
func EnsureRoleDefaults(ctx context.Context, direct *Pool, cfg config.Database) error {
	for _, stmt := range []string{
		fmt.Sprintf(`ALTER ROLE CURRENT_USER SET statement_timeout = %d`,
			cfg.StatementTimeout.Milliseconds()),
		`ALTER ROLE CURRENT_USER SET idle_in_transaction_session_timeout = 60000`,
	} {
		if _, err := direct.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("setting role defaults: %w", err)
		}
	}
	return nil
}
