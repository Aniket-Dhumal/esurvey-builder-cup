package reference

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// Loading the notified threshold yields.
//
// # Why a file and not a form
//
// A threshold yield is a statutory figure: the average of the best five of
// the last seven years for a crop in a notified unit, established by Crop
// Cutting Experiments and issued by the Department of Agriculture. It arrives
// once a season, for a whole district, as a notification -- not as fifty form
// submissions. Loading it from a file keeps the platform's copy next to the
// document it came from, reviewable and re-runnable, and records the source
// against every row so an assessment can say where its denominator came from.
//
// # What its absence costs
//
// Every assessment produced without one says, in the farmer's own language,
// that no reference yield was found and that the figure below is therefore an
// officer's recorded judgement rather than a shortfall against a notified
// yield. That sentence is honest and it is also the platform declining to do
// the thing it exists for. This is how it stops being true.
//
// # Re-running
//
// Safe, and expected. A district revises a notification; the same crop,
// season, year and unit is updated in place rather than duplicated, and the
// new issuing officer and source replace the old.

// ThresholdRow is one notified figure, as the file carries it.
type ThresholdRow struct {
	Crop         string
	UnitCode     string
	Season       string
	CropYear     string
	YieldKgPerHa float64
	Basis        string
	Source       string
}

// LoadThresholds reconciles the notified yields from a reference file.
//
// The file names a jurisdiction by code at whatever level the notification
// used -- circle where circles are mapped, taluka where they are not. The
// lookup walks up from a village and takes the most specific figure that
// exists (see baseline.ThresholdYieldFor), so a taluka figure covers every
// village under it and a circle figure overrides it for its own villages.
func LoadThresholds(
	ctx context.Context, pool *db.Pool, path string, log *slog.Logger,
) (*Result, error) {
	started := time.Now()

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening threshold file: %w", err)
	}
	defer f.Close()

	rows, err := parseThresholds(f)
	if err != nil {
		return nil, err
	}

	res := &Result{Read: len(rows)}

	// One transaction. Half a season's yields loaded is worse than none: the
	// villages that got one produce shortfalls and the villages that did not
	// produce judgements, and the two read identically on a district's
	// summary.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	for _, r := range rows {
		var jurisdictionID string
		err := tx.QueryRow(ctx,
			`SELECT id::text FROM jurisdictions WHERE code = $1`, r.UnitCode).
			Scan(&jurisdictionID)
		if err != nil {
			// Named, not counted. A unit code that does not resolve is a
			// whole taluka's worth of claims that will be assessed without a
			// denominator, and an operator needs to know which.
			res.Skipped = append(res.Skipped, fmt.Sprintf(
				"%s / %s %s: no jurisdiction with code %q",
				r.Crop, r.Season, r.CropYear, r.UnitCode))
			continue
		}

		tag, err := tx.Exec(ctx, `
			INSERT INTO threshold_yields
				(crop, jurisdiction_id, season, crop_year,
				 yield_kg_per_ha, basis, source)
			VALUES ($1, $2::uuid, $3, $4, $5, $6, $7)
			ON CONFLICT (crop, jurisdiction_id, season, crop_year)
			DO UPDATE SET
				yield_kg_per_ha = EXCLUDED.yield_kg_per_ha,
				basis           = EXCLUDED.basis,
				source          = EXCLUDED.source,
				issued_at       = now()`,
			r.Crop, jurisdictionID, r.Season, r.CropYear,
			r.YieldKgPerHa, r.Basis, r.Source)
		if err != nil {
			return nil, fmt.Errorf("loading %s for %s: %w", r.Crop, r.UnitCode, err)
		}
		// One row affected either way; the distinction worth reporting is
		// how many the file carried and how many could not be placed.
		if tag.RowsAffected() > 0 {
			res.Inserted++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	res.Duration = time.Since(started)
	if log != nil {
		log.Info("threshold yields loaded",
			"read", res.Read, "loaded", res.Inserted, "skipped", len(res.Skipped))
	}
	return res, nil
}

// parseThresholds reads the file.
//
// Columns, by name so the order in the notification does not matter:
//
//	crop, unit_code, season, crop_year, yield_kg_per_ha, basis, source
func parseThresholds(r io.Reader) ([]ThresholdRow, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	head, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading the header row: %w", err)
	}
	at := map[string]int{}
	for i, h := range head {
		at[strings.ToLower(strings.TrimSpace(h))] = i
	}
	for _, want := range []string{
		"crop", "unit_code", "season", "crop_year", "yield_kg_per_ha",
	} {
		if _, ok := at[want]; !ok {
			return nil, fmt.Errorf("the file has no %q column", want)
		}
	}

	field := func(rec []string, name string) string {
		i, ok := at[name]
		if !ok || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	var out []ThresholdRow
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		yield, err := strconv.ParseFloat(field(rec, "yield_kg_per_ha"), 64)
		if err != nil {
			return nil, fmt.Errorf(
				"line %d: %q is not a yield in kg per hectare",
				line, field(rec, "yield_kg_per_ha"))
		}
		if yield <= 0 {
			return nil, fmt.Errorf("line %d: a threshold yield of %v is not a figure "+
				"anything can be compared against", line, yield)
		}

		row := ThresholdRow{
			Crop:         field(rec, "crop"),
			UnitCode:     field(rec, "unit_code"),
			Season:       field(rec, "season"),
			CropYear:     field(rec, "crop_year"),
			YieldKgPerHa: yield,
			Basis:        field(rec, "basis"),
			Source:       field(rec, "source"),
		}
		if row.Crop == "" || row.UnitCode == "" || row.Season == "" || row.CropYear == "" {
			return nil, fmt.Errorf(
				"line %d: crop, unit_code, season and crop_year are all required",
				line)
		}
		// The basis is how the issuing body arrived at the figure, and two
		// districts may use different bases in one year. An assessment that
		// cannot say which cannot be reproduced, so it is required here even
		// though the column is nullable in the table.
		if row.Basis == "" {
			return nil, fmt.Errorf(
				"line %d: basis is required -- how the figure was arrived at, "+
					"in the issuing body's own words", line)
		}
		out = append(out, row)
	}
	return out, nil
}
