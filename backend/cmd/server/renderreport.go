package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/api"
	"github.com/agribid/esurvey-platform/backend/internal/config"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/store"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
	"github.com/agribid/esurvey-platform/backend/internal/vision"
)

// renderReport prints one claim's assessment document to stdout.
//
//	esurvey-server render-report CLR-2026-496-00001 > report.html
//
// # Why this exists
//
// The document decides whether somebody is paid, and until now the only way to
// read one was through the console with a signed-in officer's session. The
// runtime image is distroless, so checking what a report actually says meant
// minting a session against Redis by hand -- which is enough friction that
// nobody reviews the wording, and the wording is most of what the document is.
//
// It renders through the same builder the officer's print and the farmer's copy
// use. A second rendering path would drift from the real one and reviewing it
// would be worse than not reviewing at all.
//
// Read-only. It records no audit event, because nothing was disclosed to anybody
// who could not already read the database.
func renderReport(reference string) int {
	// The document goes to stdout, so every word of diagnostics must go to
	// stderr or it lands inside the HTML.
	//
	// This was not hypothetical: the first render of a real claim produced a
	// 377 KB file whose first two lines were warning records about a missing
	// frame source, which a browser renders as text above the masthead. A
	// report is a document somebody files; it cannot have logs in it.
	log := slog.New(slog.NewTextHandler(os.Stderr,
		&slog.HandlerOptions{Level: slog.LevelWarn}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration", "error", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := db.OpenFor(ctx, cfg.Database, db.Direct)
	if err != nil {
		log.Error("database", "error", err)
		return 1
	}
	defer pool.Close()

	// The whole state, read once from the jurisdiction tree rather than
	// asserted. A scope that admits nothing returns "not found" for a claim
	// that is plainly there, which reads as a missing claim rather than as a
	// missing permission.
	var stateID string
	err = pool.QueryRow(ctx,
		`SELECT id::text FROM jurisdictions WHERE level = 'state' LIMIT 1`).
		Scan(&stateID)
	if err != nil {
		log.Error("no state jurisdiction is loaded, so nothing is in scope",
			"error", err)
		return 1
	}

	// Only the stores the document is built from. Everything else a running
	// server holds -- the AI client, the rate limiter, the session store -- has
	// no part in rendering one and is left nil deliberately, so a change that
	// starts depending on them fails here rather than in a farmer's copy.
	// The captures store needs it to list a report's photographs. Their bytes
	// are not read here -- the document cites hashes and filenames -- but the
	// store is not constructible without one.
	evidenceStore, err := openEvidenceStore(ctx, cfg)
	if err != nil {
		log.Error("evidence store", "error", err)
		return 1
	}

	captures := survey.NewStore(pool, evidenceStore).WithLogger(log)

	// The frame source, so §14's plates render.
	//
	// Without it every plate is a warning and the document arrives with no
	// pictures of the crop -- which for a report whose figure rests on ground
	// imagery is the part a reviewer most wants to see. Frames come from the
	// blob cache the vision client already wrote them to, so this reads what is
	// stored rather than calling the service.
	var frames api.ClipFrames
	if cfg.VisionServiceURL != "" {
		vc := vision.New(cfg.VisionServiceURL, cfg.InternalToken, captures,
			cfg.Env == config.Development)
		vc.WithFrameCache(evidenceStore)
		frames = vc
	}

	srv := &api.Server{
		Log:       log,
		Store:     store.New(pool),
		Reports:   report.NewStore(pool),
		Captures:  captures,
		Fieldwork: fieldwork.NewStore(pool),
		Frames:    frames,
	}

	err = srv.RenderClaimReport(ctx, reference,
		rbac.Scope{JurisdictionIDs: []string{stateID}}, os.Stdout)
	if err != nil {
		// To stderr, so a caller redirecting stdout to a file gets an empty
		// file and a visible reason rather than an HTML fragment ending
		// mid-document.
		fmt.Fprintf(os.Stderr, "could not render %s: %v\n", reference, err)
		return 1
	}
	return 0
}
