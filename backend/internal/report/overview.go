package report

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Overview is the rollup an oversight role actually needs.
//
// # Why this exists
//
// A State Officer and a District Magistrate hold read and oversight
// permissions over thousands of reports, and until now the console offered
// them the same paginated list a Talathi uses to work through nine. A list is
// the wrong instrument for oversight: the question is not "what is report
// 41 doing" but "which talukas have not started", "how much is sitting
// unverified", and "where is the arbitration piling up".
//
// # Why it is scoped exactly like every other read
//
// The same jurisdiction predicate as the listings. A rollup is a place where
// scoping is easy to get wrong and hard to notice -- a leak here does not
// show one report from the next district, it shows a count that quietly
// includes it. So the filter is the shared one, pushed into the query.
type Overview struct {
	// Reports by workflow state, and the total.
	Reports      map[string]int `json:"reports"`
	ReportsTotal int            `json:"reports_total"`

	// Area claimed, in hectares. The figure a budget conversation starts from.
	AreaHectares float64 `json:"area_hectares"`

	// Farmers who have filed at least one report. Not the same as reports:
	// one farmer with four parcels is one household.
	Farmers int `json:"farmers"`

	// Events open now, and how many reports they have drawn.
	OpenEvents int `json:"open_events"`

	// Awaiting is what someone must act on. Deliberately not "pending":
	// these are counts of work, and each has an owner.
	AwaitingAcknowledgement int `json:"awaiting_acknowledgement"`
	AwaitingInspection      int `json:"awaiting_inspection"`

	// FiledLate is a count of reports submitted outside the event window.
	// They are accepted with a reason and adjudicated separately, so they are
	// a workload of their own rather than an error rate.
	FiledLate int `json:"filed_late"`

	// Areas is the breakdown one level down: talukas for a district, villages
	// for a taluka. Empty for a Talathi, whose whole posting is one village.
	Areas []AreaRollup `json:"areas"`

	// GeneratedAt so a screen showing a number can say how old it is.
	GeneratedAt time.Time `json:"generated_at"`
}

// AreaRollup is one child jurisdiction's share.
type AreaRollup struct {
	Code         string  `json:"code"`
	Name         string  `json:"name"`
	Level        string  `json:"level"`
	Reports      int     `json:"reports"`
	Farmers      int     `json:"farmers"`
	AreaHectares float64 `json:"area_hectares"`
	// Unresolved is what has not reached a terminal state. The column an
	// oversight officer sorts by.
	Unresolved int `json:"unresolved"`
}

// Overviewer builds rollups.
func (s *Store) Overview(ctx context.Context, scope rbac.Scope, eventID string) (*Overview, error) {
	if scope.Empty() {
		// An empty scope is not an error and not zero-with-a-shrug: it is a
		// correct, empty answer. Returning an error here would make a farmer
		// with no reports look like a broken page.
		return &Overview{
			Reports:     map[string]int{},
			Areas:       []AreaRollup{},
			GeneratedAt: time.Now().UTC(),
		}, nil
	}

	jur := nullableUUIDs(scope.JurisdictionIDs)
	farmer := nullString(scope.FarmerID)
	event := nullString(eventID)

	out := &Overview{
		Reports:     map[string]int{},
		Areas:       []AreaRollup{},
		GeneratedAt: time.Now().UTC(),
	}

	// --- headline counts ----------------------------------------------------
	rows, err := s.readPool().Query(ctx, `
		SELECT r.state, count(*), count(DISTINCT r.farmer_id),
		       coalesce(sum(a.affected), 0),
		       count(*) FILTER (WHERE r.filed_late)
		FROM crop_loss_reports r
		LEFT JOIN LATERAL (
			SELECT sum(i.area_affected_ha) AS affected
			FROM crop_loss_report_items i WHERE i.report_id = r.id
		) a ON true
		WHERE `+scopePredicate("$1", "$2")+`
		  AND ($3::text IS NULL OR r.survey_event_id::text = $3)
		GROUP BY r.state`, jur, farmer, event)
	if err != nil {
		return nil, fmt.Errorf("rolling up reports: %w", err)
	}
	defer rows.Close()

	// Farmers are summed per state here, which double-counts a household that
	// has reports in two states. Corrected by a separate distinct count below
	// rather than left as a plausible-looking wrong number.
	for rows.Next() {
		var state string
		var n, farmers, late int
		var area float64
		if err := rows.Scan(&state, &n, &farmers, &area, &late); err != nil {
			return nil, err
		}
		out.Reports[state] = n
		out.ReportsTotal += n
		out.AreaHectares += area
		out.FiledLate += late

		switch State(state) {
		case StateSubmitted:
			out.AwaitingAcknowledgement += n
		case StateAcknowledged:
			out.AwaitingInspection += n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if err := s.readPool().QueryRow(ctx, `
		SELECT count(DISTINCT r.farmer_id)
		FROM crop_loss_reports r
		WHERE `+scopePredicate("$1", "$2")+`
		  AND ($3::text IS NULL OR r.survey_event_id::text = $3)`,
		jur, farmer, event).Scan(&out.Farmers); err != nil {
		return nil, fmt.Errorf("counting farmers: %w", err)
	}

	if err := s.readPool().QueryRow(ctx, `
		SELECT count(*) FROM survey_events e
		-- Paused counts. It is an event a district is still running: its
		-- deadline has not moved and its reports are still climbing the
		-- ladder, so dropping it from this figure would tell a Collector who
		-- suspended collection for two days that they have no events at all.
		--
		-- Scoped as the events list is: the district inside the caller's area
		-- (a state officer's districts), or the caller's area inside the
		-- district (a Tehsildar's or Talathi's own). It used to accept only
		-- the first, so everyone below a Collector counted zero open events;
		-- and a missing jurisdiction list read as "no restriction", so a
		-- farmer was shown every open event in every state.
		WHERE e.state IN ('declared', 'open', 'paused')
		  AND $1::uuid[] IS NOT NULL AND EXISTS (
		      SELECT 1 FROM jurisdictions j
		      WHERE j.id = e.district_id
		        AND (j.id = ANY($1) OR j.ancestor_ids && $1
		             OR EXISTS (SELECT 1 FROM jurisdictions cj
		                         WHERE cj.id = ANY($1) AND j.id = ANY(cj.ancestor_ids))))`,
		jur).Scan(&out.OpenEvents); err != nil {
		// An event-table shape change should not blank the whole page.
		out.OpenEvents = 0
	}

	// --- one level down -----------------------------------------------------
	//
	// Talukas for a district, villages for a taluka. Derived from the village
	// each report sits in rather than from the officer's posting, so a
	// district with no reports in a taluka still shows that taluka absent
	// rather than inventing a zero row -- "not started" and "no such taluka"
	// look different, and only one of them is a prompt to act.
	areaRows, err := s.readPool().Query(ctx, `
		WITH scoped AS (
			SELECT r.id, r.farmer_id, r.state, v.ancestor_ids,
			       coalesce((SELECT sum(i.area_affected_ha)
			                 FROM crop_loss_report_items i
			                 WHERE i.report_id = r.id), 0) AS affected
			FROM crop_loss_reports r
			JOIN jurisdictions v
			  ON v.level = 'village' AND v.code = r.village_lgd_code
			WHERE `+scopePredicate("$1", "$2")+`
			  AND ($3::text IS NULL OR r.survey_event_id::text = $3)
		)
		SELECT parent.code, parent.name, parent.level::text,
		       count(*), count(DISTINCT scoped.farmer_id),
		       coalesce(sum(scoped.affected), 0),
		       count(*) FILTER (WHERE scoped.state NOT IN ('closed', 'withdrawn'))
		FROM scoped
		JOIN jurisdictions parent
		  ON parent.id = ANY(scoped.ancestor_ids)
		 AND parent.level = (
		     -- The level immediately below the broadest posting in scope.
		     SELECT CASE min(
		         CASE j.level
		             WHEN 'state' THEN 1 WHEN 'district' THEN 2
		             WHEN 'sub_district' THEN 3 WHEN 'circle' THEN 4
		             ELSE 5 END)
		         WHEN 1 THEN 'district' WHEN 2 THEN 'sub_district'
		         ELSE 'village' END::jurisdiction_level
		     FROM jurisdictions j WHERE $1::uuid[] IS NOT NULL AND j.id = ANY($1)
		 )
		GROUP BY parent.code, parent.name, parent.level
		ORDER BY count(*) DESC, parent.name`, jur, farmer, event)
	if err != nil {
		// The breakdown is the least important part of this page. A farmer
		// scope has no parent level at all, and a shape change here should
		// cost the table, not the headline numbers above it.
		return out, nil
	}
	defer areaRows.Close()

	for areaRows.Next() {
		var a AreaRollup
		if err := areaRows.Scan(&a.Code, &a.Name, &a.Level,
			&a.Reports, &a.Farmers, &a.AreaHectares, &a.Unresolved); err != nil {
			return out, nil
		}
		out.Areas = append(out.Areas, a)
	}
	return out, nil
}
