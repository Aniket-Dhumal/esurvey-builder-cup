// Package reference loads the administrative hierarchy.
//
// # Why this is a loader and not a migration
//
// LGD codes are reference data, not schema. They change — talukas are created,
// districts are renamed, villages are merged — and they arrive as a fresh
// export from lgdirectory.gov.in rather than as a diff. A migration is a
// one-way step in a version history; this is a periodic reconciliation, and
// running it twice must be a no-op rather than an error.
//
// # The rule that matters
//
// An LGD code is never invented. A wrong code does not fail loudly: it
// attaches a farmer to the wrong village, and every jurisdiction check
// downstream then agrees with itself while being wrong about who may see
// whose land. So the loader takes codes only from the official export, and
// refuses a row whose parent it has not already seen rather than guessing.
package reference

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// batchSize keeps one round trip per thousand rows. Maharashtra alone is
// ~45,000 villages; a round trip each would make a routine reconciliation feel
// like an outage.
const batchSize = 1000

// Node is one row of the reference file.
type Node struct {
	Level       rbac.Level
	Code        string
	Name        string
	ParentLevel rbac.Level
	ParentCode  string
}

// Result reports what a load did.
//
// Inserted and Reconciled rather than inserted/updated/unchanged: an upsert's
// RETURNING clause sees the row after the update, so it cannot tell a changed
// name from an unchanged one. Reporting a distinction the query cannot
// actually make would be worse than not reporting it.
type Result struct {
	Read       int
	Inserted   int
	Reconciled int
	Skipped    []string
	Duration   time.Duration
}

// loadOrder is the order levels must be written in, so a parent always exists
// before its children.
var loadOrder = []rbac.Level{
	rbac.LevelState, rbac.LevelDistrict, rbac.LevelSubDistrict,
	rbac.LevelCircle, rbac.LevelVillage,
}

// LoadLGD reads a flattened hierarchy file and reconciles it into the
// jurisdiction tree.
//
// The file is level,code,name,parent_level,parent_code — one row per node,
// any order. See reference_data/lgd_maharashtra.csv.
func LoadLGD(ctx context.Context, pool *db.Pool, path string, log *slog.Logger) (*Result, error) {
	started := time.Now()

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening reference file: %w", err)
	}
	defer f.Close()

	nodes, err := parse(f)
	if err != nil {
		return nil, err
	}

	res := &Result{}
	for _, batch := range nodes {
		res.Read += len(batch)
	}

	// One transaction for the whole file. A half-loaded hierarchy is worse
	// than an unloaded one: it produces villages whose taluka is missing, and
	// authorization silently narrows for everyone under them.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// ids maps (level, code) to the row id, filled as we go so a child can
	// resolve its parent without a query.
	ids, err := existingIDs(ctx, tx)
	if err != nil {
		return nil, err
	}

	for _, level := range loadOrder {
		batch := nodes[level]
		if len(batch) == 0 {
			continue
		}

		for chunk := range slices.Chunk(batch, batchSize) {
			if err := writeChunk(ctx, tx, chunk, ids, res); err != nil {
				return nil, err
			}
		}

		log.Info("reference level loaded", "level", string(level), "rows", len(batch))
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("committing reference load: %w", err)
	}

	res.Duration = time.Since(started)
	return res, nil
}

func writeChunk(
	ctx context.Context, tx pgx.Tx, chunk []Node,
	ids map[string]string, res *Result,
) error {
	b := &pgx.Batch{}
	queued := make([]Node, 0, len(chunk))

	for _, n := range chunk {
		var parentID any
		if n.Level != rbac.LevelState {
			id, ok := ids[key(n.ParentLevel, n.ParentCode)]
			if !ok {
				// Refused, not guessed. A village whose taluka is absent from
				// the export is a gap in the export, and attaching it to
				// something plausible would hide that.
				res.Skipped = append(res.Skipped, fmt.Sprintf(
					"%s %s (%s): parent %s %s not found",
					n.Level, n.Code, n.Name, n.ParentLevel, n.ParentCode))
				continue
			}
			parentID = id
		}

		b.Queue(upsertSQL, string(n.Level), n.Code, n.Name, parentID)
		queued = append(queued, n)
	}

	if len(queued) == 0 {
		return nil
	}

	results := tx.SendBatch(ctx, b)
	defer results.Close()

	for _, n := range queued {
		var id string
		var inserted bool
		if err := results.QueryRow().Scan(&id, &inserted); err != nil {
			return fmt.Errorf("writing %s %s: %w", n.Level, n.Code, err)
		}
		ids[key(n.Level, n.Code)] = id

		if inserted {
			res.Inserted++
		} else {
			res.Reconciled++
		}
	}

	return results.Close()
}

// upsertSQL reconciles one node.
//
// The parent_id clause is the whole reason this is not a plain upsert.
// Circles are not in LGD — they are locally assigned, and a village that has
// been placed under one must stay there. Re-parenting it to its taluka on the
// next reconciliation would silently unpost every Circle Officer beneath it,
// which is the kind of breakage that shows up weeks later as "why can't I see
// my villages".
const upsertSQL = `
	INSERT INTO jurisdictions (level, code, name, parent_id)
	VALUES ($1, $2, $3, $4)
	ON CONFLICT (level, code) DO UPDATE
	SET name = EXCLUDED.name,
	    parent_id = CASE
	        -- Already correct.
	        WHEN jurisdictions.parent_id IS NOT DISTINCT FROM EXCLUDED.parent_id
	            THEN jurisdictions.parent_id
	        -- Sitting under a circle that hangs off the right taluka: keep it.
	        WHEN EXISTS (
	            SELECT 1 FROM jurisdictions c
	            WHERE c.id = jurisdictions.parent_id
	              AND c.level = 'circle'
	              AND c.parent_id = EXCLUDED.parent_id
	        ) THEN jurisdictions.parent_id
	        ELSE EXCLUDED.parent_id
	    END
	-- xmax = 0 distinguishes a fresh insert from a conflict that updated.
	RETURNING id, (xmax = 0) AS inserted`

func existingIDs(ctx context.Context, tx pgx.Tx) (map[string]string, error) {
	rows, err := tx.Query(ctx, `SELECT id, level::text, code FROM jurisdictions`)
	if err != nil {
		return nil, fmt.Errorf("reading existing jurisdictions: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var id, level, code string
		if err := rows.Scan(&id, &level, &code); err != nil {
			return nil, err
		}
		out[key(rbac.Level(level), code)] = id
	}
	return out, rows.Err()
}

// parse reads the file and groups nodes by level.
func parse(r io.Reader) (map[rbac.Level][]Node, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = 5
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("reading header: %w", err)
	}

	// A byte-order mark, stripped rather than reported.
	//
	// This file is dropped in by hand, and anything saved as CSV from Excel
	// begins with one. Without this the failure is `column 1 is "\ufefflevel",
	// expected "level"` -- two strings that look identical in a terminal, on
	// the one step of the deployment somebody does manually.
	header[0] = strings.TrimPrefix(header[0], "\ufeff")

	want := []string{"level", "code", "name", "parent_level", "parent_code"}
	for i, h := range want {
		if !strings.EqualFold(strings.TrimSpace(header[i]), h) {
			return nil, fmt.Errorf(
				"column %d is %q, expected %q; the file must be %s",
				i+1, header[i], h, strings.Join(want, ","))
		}
	}

	valid := map[rbac.Level]bool{}
	for _, l := range loadOrder {
		valid[l] = true
	}

	out := map[rbac.Level][]Node{}
	seen := map[string]int{}
	line := 1

	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}

		n := Node{
			Level:       rbac.Level(strings.TrimSpace(rec[0])),
			Code:        strings.TrimSpace(rec[1]),
			Name:        strings.TrimSpace(rec[2]),
			ParentLevel: rbac.Level(strings.TrimSpace(rec[3])),
			ParentCode:  strings.TrimSpace(rec[4]),
		}

		switch {
		case !valid[n.Level]:
			return nil, fmt.Errorf("line %d: unknown level %q", line, n.Level)
		case n.Code == "":
			return nil, fmt.Errorf("line %d: empty code", line)
		case n.Name == "":
			return nil, fmt.Errorf("line %d: empty name for %s %s", line, n.Level, n.Code)
		case n.Level == rbac.LevelState && n.ParentCode != "":
			return nil, fmt.Errorf("line %d: a state has no parent", line)
		case n.Level != rbac.LevelState && (n.ParentCode == "" || n.ParentLevel == ""):
			return nil, fmt.Errorf("line %d: %s %s has no parent", line, n.Level, n.Code)
		}

		k := key(n.Level, n.Code)
		if prior, dup := seen[k]; dup {
			return nil, fmt.Errorf(
				"line %d: %s %s already appeared on line %d", line, n.Level, n.Code, prior)
		}
		seen[k] = line

		out[n.Level] = append(out[n.Level], n)
	}

	return out, nil
}

func key(level rbac.Level, code string) string {
	return string(level) + ":" + code
}
