// Package db owns the PostgreSQL connection pool and the schema migrator.
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool is the shared connection pool.
type Pool = pgxpool.Pool

// Querier is whatever can run a statement: the pool, or a transaction.
//
// It exists so a caller with a transaction in hand can pass it down. The
// outbox pattern depends on that -- the intent to notify has to commit in the
// same transaction as the thing that caused it, or a crash between the two
// loses the message with no trace.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
