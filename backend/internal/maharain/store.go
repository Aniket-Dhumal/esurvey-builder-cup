package maharain

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// Store writes what the portal published into state_rainfall.
type Store struct {
	Pool *pgxpool.Pool
}

// Saved is what one report's write did.
type Saved struct {
	Rows int
	// Matched and Unmatched count units whose published name did or did not
	// resolve to a jurisdiction this platform holds.
	Matched   int
	Unmatched []string
}

// Save writes every unit and period of one report, replacing what is there.
//
// Re-running is the normal operation: the current month's figure and the season
// total both change every day, so a row is updated in place rather than
// appended. The history this table keeps is of periods, not of fetches.
func (s *Store) Save(ctx context.Context, r *Report) (*Saved, error) {
	if r == nil || len(r.Units) == 0 {
		return nil, fmt.Errorf("nothing to save")
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The jurisdictions of this district at this level, by name.
	//
	// Resolved once per report rather than per row: a district-wide circle
	// report is 91 units times 5 periods, and 455 lookups of the same handful
	// of jurisdictions is a lot of round trips for an answer that does not
	// change.
	byName, err := s.jurisdictionsOf(ctx, tx, r)
	if err != nil {
		return nil, err
	}

	out := &Saved{}
	seenUnmatched := map[string]bool{}

	for _, u := range r.Units {
		jid, matched := byName[matchKey(u.Name)]
		if matched {
			out.Matched++
		} else if !seenUnmatched[u.Name] {
			seenUnmatched[u.Name] = true
			out.Unmatched = append(out.Unmatched, u.Name)
		}

		for i, p := range r.Periods {
			if i >= len(u.Values) {
				break
			}
			v := u.Values[i]

			var month *int
			if !p.Cumulative() {
				m := p.Month
				month = &m
			}
			var jidArg any
			if matched {
				jidArg = jid
			}

			_, err := tx.Exec(ctx, `
				INSERT INTO state_rainfall (
					unit_level, unit_name,
					source_state_code, source_division_code, source_district_code,
					district_name, jurisdiction_id,
					crop_year, period_kind, period_month, period_start, period_end,
					normal_mm, actual_mm, percent_of_normal,
					source, observed_at, fetched_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
				        $13, $14, $15, $16, $17, now())
				ON CONFLICT (source, unit_level, unit_name, source_district_code,
				             crop_year, period_kind, period_month)
				DO UPDATE SET
					normal_mm         = EXCLUDED.normal_mm,
					actual_mm         = EXCLUDED.actual_mm,
					percent_of_normal = EXCLUDED.percent_of_normal,
					period_start      = EXCLUDED.period_start,
					period_end        = EXCLUDED.period_end,
					district_name     = EXCLUDED.district_name,
					-- A row that could not be matched before and can be now is
					-- upgraded; a match already made is never cleared by a
					-- later run that failed to resolve it.
					jurisdiction_id   = COALESCE(EXCLUDED.jurisdiction_id,
					                             state_rainfall.jurisdiction_id),
					observed_at       = EXCLUDED.observed_at,
					fetched_at        = now()`,
				string(r.Level), u.Name,
				MaharashtraStateCode, r.District.DivisionCode, r.District.Code,
				r.District.Name, jidArg,
				r.CropYear, periodKind(p), month, p.Start, p.End,
				v.NormalMM, v.ActualMM, v.PercentOfNormal,
				Source, r.ObservedAt)
			if err != nil {
				return nil, fmt.Errorf("%s %s, %s: %w",
					r.Level, u.Name, p.Label, err)
			}
			out.Rows++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func periodKind(p Period) string {
	if p.Cumulative() {
		return "season_to_date"
	}
	return "month"
}

// jurisdictionsOf indexes the jurisdictions this report's units could be, by
// match key.
//
// Scoped to the district the report is for, via the district's own name, so a
// circle called "Jeur" in Solapur cannot be matched to a "Jeur" elsewhere in
// the state. Solapur genuinely has two circles called Jeur, in different
// talukas, which is why the scoping matters and why circle matching is
// best-effort.
func (s *Store) jurisdictionsOf(
	ctx context.Context, tx pgx.Tx, r *Report,
) (map[string]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT j.id::text, j.name
		  FROM jurisdictions j
		  JOIN jurisdictions d ON d.id = ANY (j.ancestor_ids)
		 WHERE j.level = $1::jurisdiction_level
		   AND d.level = 'district'
		   AND lower(d.name) = lower($2)`,
		string(r.Level), r.District.Name)
	if err != nil {
		return nil, fmt.Errorf("reading the jurisdictions of %s: %w",
			r.District.Name, err)
	}
	defer rows.Close()
	// Closed explicitly below before the alias query, because pgx holds the
	// connection until the rows are done and the second query would deadlock
	// inside the same transaction.

	// A name appearing twice inside one district cannot be matched to either,
	// and guessing would attach one circle's rainfall to another's officers.
	// Ambiguous names are dropped from the index and reported as unmatched.
	seen := map[string]int{}
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		k := matchKey(name)
		seen[k]++
		out[k] = id
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	for k, n := range seen {
		if n > 1 {
			delete(out, k)
		}
	}

	// Then the aliases, which win.
	//
	// An alias is a person's decision about what a published name means, and it
	// overrides the name match on purpose: where somebody has written down that
	// this source's "Sangola" is our "Sangole", that is more authoritative than
	// two strings happening to be equal. It also rescues the ambiguous cases --
	// a name occurring twice in a district is dropped above and can only be
	// resolved by somebody saying which one it is.
	aliases, err := tx.Query(ctx, `
		SELECT a.published_name, a.jurisdiction_id::text
		  FROM rainfall_unit_aliases a
		 WHERE a.source = $1
		   AND a.unit_level = $2::jurisdiction_level
		   AND a.source_district_code = $3`,
		Source, string(r.Level), r.District.Code)
	if err != nil {
		return nil, fmt.Errorf("reading the unit aliases: %w", err)
	}
	defer aliases.Close()

	for aliases.Next() {
		var published, id string
		if err := aliases.Scan(&published, &id); err != nil {
			return nil, err
		}
		out[matchKey(published)] = id
	}
	if err := aliases.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// matchKey is how a published name is compared with a jurisdiction's name.
//
// Case and internal spacing only. Deliberately nothing more.
//
// The portal and this platform disagree on the spelling of several circles --
// Aagalgaon against Agalgaon, Naari against Nari, Pangari against Pangri,
// Khandvi against Khandavi -- and "Upaledu" against "Upale Dumala" is not a
// spelling difference at all but a truncation. Every one of those is obvious to
// a person who knows Barshi and none of them is safely derivable by a rule: the
// same rule that joins Naari to Nari would join two genuinely different places
// elsewhere in the state.
//
// An unmatched row is stored with a null jurisdiction and reported. That is a
// name reconciliation for somebody with local knowledge to do once, and it
// leaves a record of what was decided. A transliteration heuristic would
// instead attach one revenue circle's rainfall deficit to another's farmers,
// silently, and there would be nothing in the database to show it had happened.
func matchKey(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// FetchedToday reports whether this district and level already hold figures
// fetched today, in Indian time.
//
// The portal publishes once a day: the "on day of report" column advances each
// morning and nothing else moves. Asking again the same afternoon generates a
// district-wide report on somebody's public server to receive numbers already
// stored. This is the check that makes a scheduled loader cheap, and the reason
// it can be scheduled hourly without being rude.
func (s *Store) FetchedToday(
	ctx context.Context, level Level, districtCode string, cropYear int,
) (bool, error) {
	var fresh bool
	err := s.Pool.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM state_rainfall
		   WHERE source = $1
		     AND unit_level = $2::jurisdiction_level
		     AND source_district_code = $3
		     AND crop_year = $4
		     -- Indian time, because a report published at 09:00 IST and a
		     -- loader comparing UTC days would refetch every morning.
		     AND (fetched_at AT TIME ZONE 'Asia/Kolkata')::date
		         = (now() AT TIME ZONE 'Asia/Kolkata')::date)`,
		Source, string(level), districtCode, cropYear).Scan(&fresh)
	return fresh, err
}

// --- reading it back --------------------------------------------------------

// Observation is one stored period for one unit.
type Observation struct {
	UnitLevel string  `json:"unit_level"`
	UnitName  string  `json:"unit_name"`
	District  string  `json:"district_name"`
	CropYear  int     `json:"crop_year"`
	Kind      string  `json:"period_kind"`
	Month     *int    `json:"period_month,omitempty"`
	Start     string  `json:"period_start"`
	End       string  `json:"period_end"`
	NormalMM  float64 `json:"normal_mm"`
	ActualMM  float64 `json:"actual_mm"`
	Percent   float64 `json:"percent_of_normal"`
	Source    string  `json:"source"`

	// ObservedAt is when the state said the figure was current as of.
	ObservedAt *time.Time `json:"observed_at,omitempty"`
}

// Deficit is how far below normal, in percentage points.
func (o Observation) Deficit() float64 { return 100 - o.Percent }

// ForVillage returns the published rainfall of the taluka containing a village.
//
// The taluka rather than the circle, because a village's circle is not recorded
// anywhere in this platform -- villages hang off sub_district -- and because the
// taluka is the unit drought is declared at, so it is the figure an officer will
// be asked to cite. When a village-to-circle mapping exists this is where the
// finer lookup goes.
//
// Returns nil without an error where nothing has been loaded for that unit and
// year. An absent rainfall record is an ordinary state -- a district nobody has
// run the loader for -- and the assembler reports it as an absent source rather
// than failing an assessment over it.
func (s *Store) ForVillage(
	ctx context.Context, villageLGDCode string, cropYear int,
) (*weather.Official, error) {
	if villageLGDCode == "" {
		return nil, nil
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT r.unit_level::text, r.unit_name, r.district_name, r.crop_year,
		       r.period_kind::text, r.period_month,
		       r.period_start, r.period_end,
		       r.normal_mm, r.actual_mm, r.percent_of_normal,
		       r.source, r.observed_at
		  FROM state_rainfall r
		  JOIN jurisdictions taluka ON taluka.id = r.jurisdiction_id
		  JOIN jurisdictions village
		    ON village.level = 'village'
		   AND village.code = $1
		   AND taluka.id = ANY (village.ancestor_ids)
		 WHERE r.unit_level = 'sub_district'
		   AND r.crop_year = $2
		 ORDER BY r.period_kind = 'season_to_date', r.period_month`,
		villageLGDCode, cropYear)
	if err != nil {
		return nil, fmt.Errorf(
			"reading the published rainfall for village %s: %w", villageLGDCode, err)
	}
	defer rows.Close()

	var out *weather.Official
	for rows.Next() {
		var (
			level, name, district, kind, source string
			year                                int
			month                               *int
			start, end                          time.Time
			normal, actual, pct                 float64
			observed                            *time.Time
		)
		if err := rows.Scan(&level, &name, &district, &year, &kind, &month,
			&start, &end, &normal, &actual, &pct, &source, &observed,
		); err != nil {
			return nil, err
		}

		if out == nil {
			out = &weather.Official{
				Source:      source,
				UnitLevel:   level,
				UnitName:    name,
				District:    district,
				CropYear:    year,
				PublishedAt: observed,
			}
		}

		p := weather.OfficialPeriod{
			Label:           periodLabel(month, start),
			From:            start.Format("2006-01-02"),
			To:              end.Format("2006-01-02"),
			NormalMM:        normal,
			ActualMM:        actual,
			PercentOfNormal: pct,
		}
		if kind == "season_to_date" {
			p.Label = seasonLabelOf(start, end)
			out.Season = &p
			continue
		}
		if month != nil {
			p.Month = *month
		}
		out.Months = append(out.Months, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// A record with months but no cumulative is a partial load, and the season
	// figure is the one everything downstream reads. Better to report nothing
	// than a record whose headline is missing.
	if out != nil && out.Season == nil {
		return nil, fmt.Errorf(
			"the stored rainfall for %s %d has %d months but no season total; "+
				"re-run the loader for that district",
			out.UnitName, cropYear, len(out.Months))
	}
	return out, nil
}

func periodLabel(month *int, start time.Time) string {
	if month == nil {
		return start.Format("January 2006")
	}
	return time.Month(*month).String() + " " + start.Format("2006")
}

func seasonLabelOf(start, end time.Time) string {
	return start.Format("2 January") + " to " + end.Format("2 January 2006")
}

// ForJurisdiction returns every stored period for one jurisdiction and year,
// months in order with the season-to-date last.
func (s *Store) ForJurisdiction(
	ctx context.Context, jurisdictionID string, cropYear int,
) ([]Observation, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT unit_level::text, unit_name, district_name, crop_year,
		       period_kind::text, period_month, period_start, period_end,
		       normal_mm, actual_mm, percent_of_normal, source, observed_at
		  FROM state_rainfall
		 WHERE jurisdiction_id = $1 AND crop_year = $2
		 ORDER BY period_kind = 'season_to_date', period_month`,
		jurisdictionID, cropYear)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Observation
	for rows.Next() {
		var o Observation
		var start, end time.Time
		if err := rows.Scan(&o.UnitLevel, &o.UnitName, &o.District, &o.CropYear,
			&o.Kind, &o.Month, &start, &end,
			&o.NormalMM, &o.ActualMM, &o.Percent, &o.Source, &o.ObservedAt,
		); err != nil {
			return nil, err
		}
		o.Start = start.Format("2006-01-02")
		o.End = end.Format("2006-01-02")
		out = append(out, o)
	}
	return out, rows.Err()
}
