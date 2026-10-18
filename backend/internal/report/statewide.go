package report

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Oversight above a district.
//
// # What a State Officer had before this
//
// The district dashboard, scoped upward. Every number on it was a statewide
// total, which is the one figure a state-level officer already knows, and
// there was no comparison between districts, no breakdown by crop or by event,
// and no movement over time -- which is most of what oversight at that level
// consists of. "How much has Solapur adjudicated compared with Beed" could not
// be asked.
//
// # Why the numbers come from the rollups
//
// Because this is the query that cannot be a live GROUP BY. A State Officer
// holds the whole state in scope, so the live version is a full scan of the
// largest table in the database on every page load, and the planning estimate
// is order 10^6 claims a season. Reading the rollups turns it into a scan of
// one row per village per crop per event.
//
// The cost is that a figure here is as old as the last sweep, which runs on a
// minute ticker. Said out loud on the screen rather than hidden: an aggregate
// that claims to be live and is not is worse than one that dates itself.
//
// # Why a percentage is never averaged
//
// Two districts at 60% do not make a state at 60% unless the same area was
// affected in each. The rollups store sum(percent x area) beside the area, and
// every level here divides the totals. See migration 0030.

// Statewide is the cross-district view.
type Statewide struct {
	// Districts is the comparison, one row each, ordered by area affected.
	Districts []DistrictRow `json:"districts"`
	// Crops is the same claims cut by what was growing.
	Crops []CropRow `json:"crops"`
	// Events is per notified calamity, newest first.
	Events []EventRow `json:"events"`
	// Filed is the daily count over the window, for the trend.
	Filed []DayRow `json:"filed"`

	Totals Totals `json:"totals"`

	// RefreshedAt is the oldest rollup behind these numbers, and Stale says
	// whether anything is waiting to be recomputed. Both on the screen: a
	// number with no age is read as current.
	RefreshedAt *time.Time `json:"refreshed_at,omitempty"`
	Pending     int        `json:"pending_refresh"`
	GeneratedAt time.Time  `json:"generated_at"`
}

// Totals is the state, in the five numbers a briefing starts from.
type Totals struct {
	Reports         int     `json:"reports"`
	Unresolved      int     `json:"unresolved"`
	Adjudicated     int     `json:"adjudicated"`
	Contested       int     `json:"contested"`
	AreaAffectedHa  float64 `json:"area_affected_ha"`
	AreaSownHa      float64 `json:"area_sown_ha"`
	Farmers         int     `json:"farmers"`
	DeclaredPercent float64 `json:"declared_percent"`
	// AdjudicatedPercent is weighted over the adjudicated area only. A claim
	// still climbing the ladder has no figure, and counting its area in the
	// denominator would drag the average toward zero as claims are filed.
	AdjudicatedPercent float64 `json:"adjudicated_percent"`
}

// DistrictRow is one district's share.
type DistrictRow struct {
	Code string `json:"code"`
	Name string `json:"name"`

	Reports     int `json:"reports"`
	Unresolved  int `json:"unresolved"`
	Adjudicated int `json:"adjudicated"`
	Contested   int `json:"contested"`
	FiledLate   int `json:"filed_late"`

	AreaAffectedHa float64 `json:"area_affected_ha"`
	AreaSownHa     float64 `json:"area_sown_ha"`

	DeclaredPercent    float64 `json:"declared_percent"`
	AdjudicatedPercent float64 `json:"adjudicated_percent"`

	// Villages that have filed anything, which is the reach of the event in
	// this district rather than its size.
	Villages int `json:"villages"`
}

// CropRow is the same claims cut by what was growing.
type CropRow struct {
	Crop            string  `json:"crop"`
	Reports         int     `json:"reports"`
	AreaAffectedHa  float64 `json:"area_affected_ha"`
	DeclaredPercent float64 `json:"declared_percent"`
}

// EventRow is one notified calamity.
type EventRow struct {
	ID           string    `json:"id"`
	Reference    string    `json:"reference"`
	Title        string    `json:"title"`
	CalamityType string    `json:"calamity_type"`
	State        string    `json:"state"`
	DistrictName string    `json:"district_name"`
	IncidentFrom time.Time `json:"incident_from"`

	Reports        int     `json:"reports"`
	Unresolved     int     `json:"unresolved"`
	AreaAffectedHa float64 `json:"area_affected_ha"`
}

// DayRow is one day's filings, for the trend.
type DayRow struct {
	Day     string `json:"day"`
	Reports int    `json:"reports"`
}

// TrendDays is how far back the filing trend looks.
//
// Eight weeks: long enough that a monsoon event and its tail both sit inside
// the window, short enough that the shape of last week is still legible.
const TrendDays = 56

// StatewideView builds the cross-district oversight view.
//
// Scoped like every other read. A State Officer holds the state, so in
// practice this is the whole of it; a Collector calling the same query gets
// their own district and its talukas, which is a working comparison too and
// costs nothing extra to allow.
func (s *Store) StatewideView(ctx context.Context, scope rbac.Scope, eventID string) (*Statewide, error) {
	out := &Statewide{
		Districts:   []DistrictRow{},
		Crops:       []CropRow{},
		Events:      []EventRow{},
		Filed:       []DayRow{},
		GeneratedAt: time.Now().UTC(),
	}
	if scope.Empty() {
		return out, nil
	}

	jur := nullableUUIDs(scope.JurisdictionIDs)
	event := nullString(eventID)

	// The rollups, joined to the tree so each village row knows its district
	// and can be tested against the caller's scope. ancestor_ids is already
	// indexed -- it is how every authorization check works.
	const from = `
		FROM report_rollups ru
		JOIN jurisdictions v
		  ON v.code = ru.village_lgd_code AND v.level = 'village'
		JOIN jurisdictions d
		  ON d.id = ANY(v.ancestor_ids) AND d.level = 'district'
	   WHERE (v.id = ANY($1::uuid[]) OR v.ancestor_ids && $1::uuid[])
	     AND ($2::text IS NULL OR ru.survey_event_id::text = $2)`

	// --- districts ---------------------------------------------------------
	rows, err := s.readPool().Query(ctx, `
		SELECT d.code, d.name,
		       sum(ru.reports), sum(ru.unresolved), sum(ru.adjudicated),
		       sum(ru.contested), sum(ru.filed_late),
		       sum(ru.area_affected_ha), sum(ru.area_sown_ha),
		       sum(ru.declared_pct_area), sum(ru.adjudicated_pct_area),
		       sum(ru.adjudicated_area_ha),
		       count(DISTINCT ru.village_lgd_code)
		`+from+`
		GROUP BY d.code, d.name
		ORDER BY sum(ru.area_affected_ha) DESC`, jur, event)
	if err != nil {
		return nil, fmt.Errorf("rolling up districts: %w", err)
	}
	defer rows.Close()

	var declNum, declArea, adjNum, adjArea float64
	for rows.Next() {
		var r DistrictRow
		var dNum, aNum, aArea float64
		if err := rows.Scan(&r.Code, &r.Name, &r.Reports, &r.Unresolved,
			&r.Adjudicated, &r.Contested, &r.FiledLate, &r.AreaAffectedHa,
			&r.AreaSownHa, &dNum, &aNum, &aArea, &r.Villages); err != nil {
			return nil, err
		}
		r.DeclaredPercent = weighted(dNum, r.AreaAffectedHa)
		r.AdjudicatedPercent = weighted(aNum, aArea)
		out.Districts = append(out.Districts, r)

		out.Totals.Reports += r.Reports
		out.Totals.Unresolved += r.Unresolved
		out.Totals.Adjudicated += r.Adjudicated
		out.Totals.Contested += r.Contested
		out.Totals.AreaAffectedHa += r.AreaAffectedHa
		out.Totals.AreaSownHa += r.AreaSownHa
		declNum, declArea = declNum+dNum, declArea+r.AreaAffectedHa
		adjNum, adjArea = adjNum+aNum, adjArea+aArea
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	out.Totals.DeclaredPercent = weighted(declNum, declArea)
	out.Totals.AdjudicatedPercent = weighted(adjNum, adjArea)

	// --- crops -------------------------------------------------------------
	crops, err := s.readPool().Query(ctx, `
		SELECT COALESCE(NULLIF(ru.crop_name, ''), 'Not recorded'),
		       sum(ru.reports), sum(ru.area_affected_ha),
		       sum(ru.declared_pct_area)
		`+from+`
		GROUP BY 1
		ORDER BY sum(ru.area_affected_ha) DESC
		LIMIT 20`, jur, event)
	if err != nil {
		return nil, fmt.Errorf("rolling up crops: %w", err)
	}
	defer crops.Close()
	for crops.Next() {
		var c CropRow
		var num float64
		if err := crops.Scan(&c.Crop, &c.Reports, &c.AreaAffectedHa, &num); err != nil {
			return nil, err
		}
		c.DeclaredPercent = weighted(num, c.AreaAffectedHa)
		out.Crops = append(out.Crops, c)
	}
	if err := crops.Err(); err != nil {
		return nil, err
	}
	crops.Close()

	// --- farmers -----------------------------------------------------------
	//
	// Not summed from the rollups. Each row counts distinct farmers within one
	// village and one crop, so adding them up counts a household once per
	// parcel it owns in different villages -- which is exactly the household
	// an oversight officer is asked about.
	if err := s.readPool().QueryRow(ctx, `
		SELECT count(DISTINCT r.farmer_id)
		  FROM crop_loss_reports r
		  JOIN jurisdictions v
		    ON v.code = r.village_lgd_code AND v.level = 'village'
		 WHERE (v.id = ANY($1::uuid[]) OR v.ancestor_ids && $1::uuid[])
		   AND ($2::text IS NULL OR r.survey_event_id::text = $2)`,
		jur, event).Scan(&out.Totals.Farmers); err != nil {
		return nil, fmt.Errorf("counting farmers: %w", err)
	}

	// --- events ------------------------------------------------------------
	events, err := s.readPool().Query(ctx, `
		SELECT e.id::text, e.reference, e.title, e.calamity_type::text,
		       e.state::text, COALESCE(d.name, ''), e.incident_start,
		       COALESCE(sum(ru.reports), 0), COALESCE(sum(ru.unresolved), 0),
		       COALESCE(sum(ru.area_affected_ha), 0)
		  FROM survey_events e
		  LEFT JOIN jurisdictions d ON d.id = e.district_id
		  LEFT JOIN report_rollups ru ON ru.survey_event_id = e.id
		  LEFT JOIN jurisdictions v
		         ON v.code = ru.village_lgd_code AND v.level = 'village'
		        AND (v.id = ANY($1::uuid[]) OR v.ancestor_ids && $1::uuid[])
		 WHERE (d.id = ANY($1::uuid[]) OR d.ancestor_ids && $1::uuid[]
		        OR d.id IS NULL)
		 GROUP BY e.id, e.reference, e.title, e.calamity_type, e.state,
		          d.name, e.incident_start
		 ORDER BY e.incident_start DESC
		 LIMIT 25`, jur)
	if err != nil {
		return nil, fmt.Errorf("rolling up events: %w", err)
	}
	defer events.Close()
	for events.Next() {
		var e EventRow
		if err := events.Scan(&e.ID, &e.Reference, &e.Title, &e.CalamityType,
			&e.State, &e.DistrictName, &e.IncidentFrom, &e.Reports,
			&e.Unresolved, &e.AreaAffectedHa); err != nil {
			return nil, err
		}
		out.Events = append(out.Events, e)
	}
	if err := events.Err(); err != nil {
		return nil, err
	}
	events.Close()

	// --- the trend ---------------------------------------------------------
	//
	// Read live from the claims table and not from the rollups, on purpose:
	// the rollups have no time dimension, and giving them one would mean a row
	// per village per crop per day. This is one indexed range over recent
	// rows, which is cheap however large the table gets, because the window
	// does not grow.
	days, err := s.readPool().Query(ctx, `
		SELECT to_char(d.day, 'YYYY-MM-DD'), COALESCE(f.n, 0)
		  FROM generate_series(
		           (current_date - ($3::int - 1))::date, current_date, '1 day'
		       ) AS d(day)
		  LEFT JOIN (
		      SELECT r.reported_at::date AS day, count(*) AS n
		        FROM crop_loss_reports r
		        JOIN jurisdictions v
		          ON v.code = r.village_lgd_code AND v.level = 'village'
		       WHERE (v.id = ANY($1::uuid[]) OR v.ancestor_ids && $1::uuid[])
		         AND ($2::text IS NULL OR r.survey_event_id::text = $2)
		         AND r.reported_at >= current_date - ($3::int - 1)
		       GROUP BY 1
		  ) f ON f.day = d.day
		 ORDER BY d.day`, jur, event, TrendDays)
	if err != nil {
		return nil, fmt.Errorf("reading the filing trend: %w", err)
	}
	defer days.Close()
	for days.Next() {
		var d DayRow
		if err := days.Scan(&d.Day, &d.Reports); err != nil {
			return nil, err
		}
		out.Filed = append(out.Filed, d)
	}
	if err := days.Err(); err != nil {
		return nil, err
	}
	days.Close()

	// --- how old is this ---------------------------------------------------
	var oldest *time.Time
	if err := s.readPool().QueryRow(ctx,
		`SELECT min(refreshed_at) FROM report_rollups`).Scan(&oldest); err == nil {
		out.RefreshedAt = oldest
	}
	_ = s.readPool().QueryRow(ctx,
		// Distinct pairs: the queue is append-only, so one village marked
		// five times is one piece of work, not five.
		`SELECT count(*) FROM (
			SELECT DISTINCT survey_event_id, village_lgd_code
			  FROM report_rollup_dirty) q`).Scan(&out.Pending)

	return out, nil
}

// weighted divides a numerator by the area it was weighted over.
//
// Zero when nothing has been adjudicated, rather than a division by zero or a
// misleading 0.0% -- the caller renders an empty cell, because "no figure yet"
// and "no damage" are different answers and must not look alike.
func weighted(numerator, area float64) float64 {
	if area <= 0 {
		return 0
	}
	return numerator / area
}
