package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/config"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/reference"
)

// lgdLoad reconciles the administrative hierarchy from a reference file.
//
//	esurvey-server lgd-load reference_data/lgd_maharashtra.csv
//
// A subcommand of the same binary rather than a second one, because the
// runtime image is distroless and shipping a second static binary to run twice
// a year is a worse trade than one extra branch in main.
//
// Safe to re-run. That is the point: LGD publishes a fresh export rather than
// a diff, so reconciliation is the normal operation and the loader is written
// to be boring about it.
func lgdLoad(path string) int {
	log := newLogger(config.Development)

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration", "error", err)
		return 1
	}

	// Generous: 45,000 villages through a per-row trigger is seconds, but a
	// first load on a cold database should not be racing a timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	pool, err := db.OpenFor(ctx, cfg.Database, db.Direct)
	if err != nil {
		log.Error("database", "error", err)
		return 1
	}
	defer pool.Close()

	res, err := reference.LoadLGD(ctx, pool, path, log)
	if err != nil {
		log.Error("reference load failed", "path", path, "error", err)
		return 1
	}

	log.Info("reference load complete",
		"path", path,
		"read", res.Read,
		"inserted", res.Inserted,
		"reconciled", res.Reconciled,
		"skipped", len(res.Skipped),
		"duration", res.Duration.Round(time.Millisecond).String())

	// Skipped rows are printed in full rather than counted. Each one is a
	// village nobody will be able to file a claim for, and a count alone does
	// not tell an operator which.
	for _, s := range res.Skipped {
		fmt.Fprintln(os.Stderr, "  skipped: "+s)
	}
	if len(res.Skipped) > 0 {
		return 2
	}

	return 0
}

// thresholdLoad loads the notified threshold yields from a reference file.
//
//	esurvey-server threshold-load reference_data/threshold_yields.csv
//
// Safe to re-run: a district revises a notification and the same crop,
// season, year and unit is updated in place. See reference.LoadThresholds for
// why this is a file rather than a form.
func thresholdLoad(path string) int {
	log := newLogger(config.Development)

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration", "error", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pool, err := db.OpenFor(ctx, cfg.Database, db.Direct)
	if err != nil {
		log.Error("database", "error", err)
		return 1
	}
	defer pool.Close()

	res, err := reference.LoadThresholds(ctx, pool, path, log)
	if err != nil {
		log.Error("threshold load failed", "path", path, "error", err)
		return 1
	}

	log.Info("threshold load complete",
		"path", path, "read", res.Read, "loaded", res.Inserted,
		"skipped", len(res.Skipped),
		"duration", res.Duration.Round(time.Millisecond).String())

	// Each skipped row is a notified unit whose claims will be assessed with
	// no denominator, and a count alone does not say which.
	for _, s := range res.Skipped {
		fmt.Fprintln(os.Stderr, "  skipped: "+s)
	}
	if len(res.Skipped) > 0 {
		return 2
	}
	return 0
}

func usage(log *slog.Logger) {
	log.Error("usage: esurvey-server [healthcheck | lgd-load <file> | " +
		"threshold-load <file> | rainfall-load [district] | " +
		"render-report <reference>]")
}
