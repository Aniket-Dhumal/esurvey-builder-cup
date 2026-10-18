// Package cache owns the Redis client.
//
// Redis holds this system's transient security state: refresh tokens, one-time
// codes, failed-login counters and lockouts. That split is deliberate.
// PostgreSQL holds who someone is and what they may do -- facts that must
// survive anything. Redis holds facts that expire on their own, where a TTL is
// the enforcement mechanism rather than a cleanup job, and where losing the
// lot means everyone signs in again rather than anyone losing an entitlement.
package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/agribid/esurvey-platform/backend/internal/config"
)

// Client is the shared Redis client.
type Client = redis.Client

// Nil is returned by lookups that found nothing.
const Nil = redis.Nil

// Open builds a client and proves it is reachable.
func Open(ctx context.Context, cfg config.Redis) (*Client, error) {
	c := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		PoolSize:     cfg.PoolSize,
		MinIdleConns: 2,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
		// A sign-in should fail fast and be retried by the caller rather than
		// queue behind a stalled connection.
		PoolTimeout:     4 * time.Second,
		MaxRetries:      2,
		MinRetryBackoff: 20 * time.Millisecond,
		MaxRetryBackoff: 200 * time.Millisecond,
	})

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.Ping(pingCtx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("reaching redis at %s: %w", cfg.Addr, err)
	}

	return c, nil
}
