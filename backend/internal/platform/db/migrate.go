package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// advisoryLockKey is an arbitrary constant, shared by every replica of this
// service and by nothing else.
const advisoryLockKey int64 = 7_314_552_019_384_112

// Migration is one numbered SQL file.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// Migrate applies every pending migration.
//
// Written here rather than pulled in as a dependency because the whole of it
// is the three properties that matter and each is worth being able to read:
//
//   - one replica at a time, via a session advisory lock, so a rolling deploy
//     of five containers does not run the same CREATE TABLE five times;
//   - one transaction per migration, so a failure leaves the schema at a
//     version that exists rather than half of one that does not;
//   - a checksum per applied file, so an edit to a migration that has already
//     run is caught at startup instead of becoming a difference between two
//     environments that nobody can explain.
func Migrate(ctx context.Context, pool *Pool, log *slog.Logger) error {
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if len(migrations) == 0 {
		return errors.New("no migrations found; the embedded directory is empty")
	}

	// A dedicated connection: an advisory lock is held by the session, and a
	// pooled connection could be handed to someone else mid-migration.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring migration connection: %w", err)
	}
	defer conn.Release()

	// Migrations legitimately take longer than the pool's statement timeout;
	// a large index build is not a runaway query.
	if _, err := conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return fmt.Errorf("clearing statement timeout: %w", err)
	}

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("acquiring migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, advisoryLockKey); err != nil {
			log.Warn("releasing migration lock", "error", err)
		}
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     integer PRIMARY KEY,
			name        text NOT NULL,
			checksum    text NOT NULL,
			applied_at  timestamptz NOT NULL DEFAULT now(),
			duration_ms integer NOT NULL
		)`); err != nil {
		return fmt.Errorf("creating schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn.Conn())
	if err != nil {
		return err
	}

	for _, m := range migrations {
		if prior, ok := applied[m.Version]; ok {
			if prior != m.Checksum {
				return fmt.Errorf(
					"migration %04d_%s has changed since it was applied "+
						"(recorded %s, now %s); an applied migration is history, "+
						"so write a new one instead",
					m.Version, m.Name, short(prior), short(m.Checksum))
			}
			continue
		}

		started := time.Now()
		if err := applyOne(ctx, conn.Conn(), m); err != nil {
			return fmt.Errorf("migration %04d_%s: %w", m.Version, m.Name, err)
		}
		log.Info("migration applied",
			"version", m.Version,
			"name", m.Name,
			"duration", time.Since(started).Round(time.Millisecond).String())
	}

	return nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	started := time.Now()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO schema_migrations (version, name, checksum, duration_ms)
		VALUES ($1, $2, $3, $4)`,
		m.Version, m.Name, m.Checksum, time.Since(started).Milliseconds()); err != nil {
		return fmt.Errorf("recording migration: %w", err)
	}

	return tx.Commit(ctx)
}

func appliedVersions(ctx context.Context, conn *pgx.Conn) (map[int]string, error) {
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("reading schema_migrations: %w", err)
	}
	defer rows.Close()

	out := map[int]string{}
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, err
		}
		out[v] = sum
	}
	return out, rows.Err()
}

// loadMigrations reads and orders the embedded files. Names are NNNN_name.sql.
func loadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("reading migrations: %w", err)
	}

	var out []Migration
	seen := map[int]string{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}

		base := strings.TrimSuffix(e.Name(), ".sql")
		num, name, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q is not named NNNN_name.sql", e.Name())
		}
		version, err := strconv.Atoi(num)
		if err != nil {
			return nil, fmt.Errorf("migration %q has a non-numeric version", e.Name())
		}
		if prior, dup := seen[version]; dup {
			return nil, fmt.Errorf(
				"migrations %q and %q share version %d; two branches merged "+
					"without renumbering", prior, e.Name(), version)
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)

		out = append(out, Migration{
			Version:  version,
			Name:     name,
			SQL:      string(body),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}
