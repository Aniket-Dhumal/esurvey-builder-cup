package api

import (
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// How much of the state has its circles recorded.
//
// # Why this is measured rather than assumed
//
// A circle is a revenue unit and LGD assigns it no code, so the
// village-to-circle mapping is in no national directory and is not published
// as data anywhere this project could find. It exists in Settlement
// Commissioner and district revenue records. Solapur's own site publishes the
// counts -- 91 circles across eleven talukas -- and not the names, and not
// which village belongs to which.
//
// The consequence is quiet and it compounds. A village with no circle above it
// has no Circle Officer, so the first genuinely independent reviewer in the
// chain is skipped and the claim goes to the Tehsildar. Nothing fails, nothing
// is logged, and the ladder simply has one fewer rung in most of the state.
//
// # Why nothing here fills the gap in
//
// Because a wrong mapping is worse than a missing one. An invented circle
// attaches a village to a place that does not administer it, and every
// jurisdiction check downstream then agrees with itself while being wrong
// about who may see whose land. The rule this data is governed by is that a
// code is never invented; the same reasoning covers a parentage.
//
// So this counts what is there, says what is not, and leaves the filling-in to
// the loader and a revenue record. See reference_data/README.md.

type talukaCoverage struct {
	DistrictName string `json:"district_name"`
	DistrictCode string `json:"district_code"`
	TalukaName   string `json:"taluka_name"`
	TalukaCode   string `json:"taluka_code"`

	Circles int `json:"circles"`
	// VillagesUnderCircle and VillagesDirect together are the taluka's
	// villages. The second is the number with no Circle Officer above them.
	VillagesUnderCircle int `json:"villages_under_circle"`
	VillagesDirect      int `json:"villages_direct"`
}

// circleCoverage reports, per taluka, how much of the circle tier exists.
func (s *Server) circleCoverage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	// The operator's view is the whole country; an officer's is their own
	// area. It returned every state's talukas to anyone who could read the
	// reference data.
	var scope any
	if !p.Can(rbac.PermPlatformRead) {
		// An empty list, never nil: nil is the operator's "everywhere", and an
		// officer with no posting has nowhere.
		ids := p.Scope().JurisdictionIDs
		if ids == nil {
			ids = []string{}
		}
		scope = ids
	}

	// One pass over the tree. Villages hang either off a taluka directly or
	// off a circle that hangs off the taluka, so the taluka is found by
	// looking one level up and, failing that, two.
	rows, err := s.readDB().Query(ctx, `
		WITH taluka AS (
		    SELECT t.id, t.code, t.name, d.code AS district_code, d.name AS district_name
		      FROM jurisdictions t
		      JOIN jurisdictions d ON d.id = t.parent_id AND d.level = 'district'
		     WHERE t.level = 'sub_district'
		       AND ($1::uuid[] IS NULL
		            OR t.id = ANY($1) OR t.ancestor_ids && $1
		            OR EXISTS (SELECT 1 FROM jurisdictions cj
		                        WHERE cj.id = ANY($1) AND t.id = ANY(cj.ancestor_ids)))
		),
		circles AS (
		    SELECT parent_id AS taluka_id, count(*) AS n
		      FROM jurisdictions WHERE level = 'circle'
		     GROUP BY parent_id
		),
		villages AS (
		    SELECT COALESCE(c.parent_id, v.parent_id) AS taluka_id,
		           count(*) FILTER (WHERE c.id IS NOT NULL) AS under_circle,
		           count(*) FILTER (WHERE c.id IS NULL)     AS direct
		      FROM jurisdictions v
		      LEFT JOIN jurisdictions c
		             ON c.id = v.parent_id AND c.level = 'circle'
		     WHERE v.level = 'village'
		     GROUP BY 1
		)
		SELECT t.district_name, t.district_code, t.name, t.code,
		       COALESCE(circles.n, 0),
		       COALESCE(villages.under_circle, 0),
		       COALESCE(villages.direct, 0)
		  FROM taluka t
		  LEFT JOIN circles  ON circles.taluka_id  = t.id
		  LEFT JOIN villages ON villages.taluka_id = t.id
		 ORDER BY t.district_name, t.name`, scope)
	if err != nil {
		s.Log.Error("reading circle coverage", "error", err)
		writeError(w, http.StatusInternalServerError, "COVERAGE_FAILED", "")
		return
	}
	defer rows.Close()

	out := []talukaCoverage{}
	var talukas, withCircles, villages, uncovered int
	for rows.Next() {
		var c talukaCoverage
		if err := rows.Scan(&c.DistrictName, &c.DistrictCode, &c.TalukaName,
			&c.TalukaCode, &c.Circles, &c.VillagesUnderCircle,
			&c.VillagesDirect); err != nil {
			s.Log.Error("scanning circle coverage", "error", err)
			writeError(w, http.StatusInternalServerError, "COVERAGE_FAILED", "")
			return
		}
		talukas++
		if c.Circles > 0 {
			withCircles++
		}
		villages += c.VillagesUnderCircle + c.VillagesDirect
		uncovered += c.VillagesDirect
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		s.Log.Error("reading circle coverage", "error", err)
		writeError(w, http.StatusInternalServerError, "COVERAGE_FAILED", "")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"talukas": out,
		"summary": map[string]any{
			"talukas":             talukas,
			"talukas_with_circle": withCircles,
			"villages":            villages,
			"villages_no_circle":  uncovered,
			"note": "A village with no circle above it has no Circle Officer, " +
				"and its claims reach the Tehsildar with one fewer independent " +
				"review behind them. Circles are loaded from a district revenue " +
				"record; see reference_data/README.md. Codes and parentages are " +
				"never invented.",
		},
	})
}
