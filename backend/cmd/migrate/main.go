// Command migrate applies the schema to a database and stops.
//
// # Why this exists separately from the server
//
// The server migrates on start, which is right for a deployment and wrong for
// three things it is regularly asked to do:
//
//   - a restore drill, where the point is to prove a backup plus the schema
//     produces a working database, without an API in front of it;
//   - a test database, which needs the schema and nothing else;
//   - reading what a migration will do before letting a release do it.
//
// Each of those was previously a matter of starting the whole server against
// the target database and stopping it once the log said it was listening,
// which also opened a port, connected to Redis, and ran the hardening checks.
//
// It takes the database URL from ESURVEY_DATABASE_URL, or from -url.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/config"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

func main() {
	url := flag.String("url", os.Getenv("ESURVEY_DATABASE_URL"),
		"database url; defaults to ESURVEY_DATABASE_URL")
	timeout := flag.Duration("timeout", 5*time.Minute, "how long to allow")
	flag.Parse()

	if *url == "" {
		fmt.Fprintln(os.Stderr,
			"no database url: set ESURVEY_DATABASE_URL or pass -url")
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	pool, err := db.Open(ctx, config.Database{
		URL: *url, MaxConns: 4, MinConns: 1,
		MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute,
		// Generous: an index built on a table with a season of claims in it
		// takes longer than any query the API would ever run.
		StatementTimeout: *timeout,
	})
	if err != nil {
		// The URL carries a password. Printing the error is fine -- pgx does
		// not put the password in it -- but the URL itself never is.
		log.Error("connecting", "error", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := db.Migrate(ctx, pool, log); err != nil {
		log.Error("migrating", "error", err)
		os.Exit(1)
	}
	log.Info("schema is up to date")
}
