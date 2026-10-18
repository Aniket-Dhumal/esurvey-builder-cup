package reference

import (
	"context"
	"fmt"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
)

// How much of the hierarchy is actually mapped.
//
// # Why a circle is not a detail
//
// A circle (मंडळ, or saza) sits between the taluka and the village, and the
// Circle Officer posted to it is the first genuinely independent reviewer in
// the whole verification chain: the Talathi and the Krushi Sahayak share a
// village and often an office, and the Circle Officer does not.
//
// Where a district's circles are not loaded, no Circle Officer can be posted
// there, an arbitration between two disagreeing village officers falls
// through to the Tehsildar, and a district can only declare an event over
// whole talukas -- a level coarser than the unit the decision is actually
// made in.
//
// LGD publishes the hierarchy down to sub-district and resumes at village.
// The village-to-circle mapping is not in it, which is why 357 of 358 talukas
// have none and why this exists: an absence nobody can see is an absence
// nobody fixes.
//
// # Why this reports and does not import
//
// The mapping is a district record, obtained from a district. The loader
// already accepts it in the same format as everything else, and already
// re-parents a village correctly without undoing the work when the base LGD
// file is loaded again. What was missing was any way to see how far the work
// had got.

// Coverage is the mapped share of one jurisdiction.
type Coverage struct {
	Code string `json:"code"`
	Name string `json:"name"`

	SubDistricts            int `json:"sub_districts"`
	SubDistrictsWithCircles int `json:"sub_districts_with_circles"`

	Circles int `json:"circles"`

	Villages          int `json:"villages"`
	VillagesInCircles int `json:"villages_in_circles"`

	// Percent is the share of villages that sit under a circle, which is the
	// figure that decides whether a Circle Officer can be posted for them.
	Percent float64 `json:"villages_mapped_percent"`
}

// CoverageReport is the state of the hierarchy, and what it costs.
type CoverageReport struct {
	State     Coverage   `json:"state"`
	Districts []Coverage `json:"districts"`

	// Consequences are what the gaps mean operationally, in the words an
	// officer would use rather than as a percentage.
	Consequences []string `json:"consequences"`
}

// CircleCoverage reports how much of Maharashtra has its circles mapped.
func CircleCoverage(ctx context.Context, pool *db.Pool) (*CoverageReport, error) {
	rows, err := pool.Query(ctx, `
		WITH district AS (
			SELECT id, code, name FROM jurisdictions WHERE level = 'district'
		),
		counts AS (
			SELECT d.id, d.code, d.name,
			       count(*) FILTER (WHERE j.level = 'sub_district')  AS sub_districts,
			       count(*) FILTER (WHERE j.level = 'circle')        AS circles,
			       count(*) FILTER (WHERE j.level = 'village')       AS villages,
			       -- A village is mapped when its immediate parent is a
			       -- circle. Anything else -- hanging off the taluka -- is a
			       -- village no Circle Officer can be posted over.
			       count(*) FILTER (
			           WHERE j.level = 'village' AND p.level = 'circle'
			       ) AS villages_in_circles,
			       count(DISTINCT c.parent_id) FILTER (
			           WHERE j.level = 'circle'
			       ) AS talukas_with_circles
			FROM district d
			LEFT JOIN jurisdictions j ON d.id = ANY (j.ancestor_ids)
			LEFT JOIN jurisdictions p ON p.id = j.parent_id
			LEFT JOIN jurisdictions c ON c.id = j.id AND j.level = 'circle'
			GROUP BY d.id, d.code, d.name
		)
		SELECT code, name, sub_districts, talukas_with_circles, circles,
		       villages, villages_in_circles
		FROM counts
		ORDER BY villages_in_circles::numeric / NULLIF(villages, 0) NULLS FIRST,
		         name`)
	if err != nil {
		return nil, fmt.Errorf("reading circle coverage: %w", err)
	}
	defer rows.Close()

	rep := &CoverageReport{Districts: []Coverage{}}
	for rows.Next() {
		var c Coverage
		if err := rows.Scan(&c.Code, &c.Name, &c.SubDistricts,
			&c.SubDistrictsWithCircles, &c.Circles,
			&c.Villages, &c.VillagesInCircles); err != nil {
			return nil, err
		}
		if c.Villages > 0 {
			c.Percent = round1(float64(c.VillagesInCircles) / float64(c.Villages) * 100)
		}

		rep.State.SubDistricts += c.SubDistricts
		rep.State.SubDistrictsWithCircles += c.SubDistrictsWithCircles
		rep.State.Circles += c.Circles
		rep.State.Villages += c.Villages
		rep.State.VillagesInCircles += c.VillagesInCircles

		rep.Districts = append(rep.Districts, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rep.State.Name = "Maharashtra"
	if rep.State.Villages > 0 {
		rep.State.Percent = round1(
			float64(rep.State.VillagesInCircles) / float64(rep.State.Villages) * 100)
	}
	rep.Consequences = consequencesOf(rep)
	return rep, nil
}

func consequencesOf(rep *CoverageReport) []string {
	var out []string
	s := rep.State

	unmapped := s.Villages - s.VillagesInCircles
	if unmapped > 0 {
		out = append(out, fmt.Sprintf(
			"%s of %s villages have no circle. No Circle Officer can be posted "+
				"over them, so where a Talathi and a Krushi Sahayak disagree the "+
				"arbitration falls through to the Tehsildar -- who is one officer "+
				"for a whole taluka.",
			commas(unmapped), commas(s.Villages)))
	}

	if without := s.SubDistricts - s.SubDistrictsWithCircles; without > 0 {
		out = append(out, fmt.Sprintf(
			"%d of %d talukas have no circles loaded. A district there can only "+
				"declare an event over whole talukas, which is a level coarser "+
				"than the unit the decision is made in.",
			without, s.SubDistricts))
	}

	var complete, started int
	for _, d := range rep.Districts {
		switch {
		case d.Villages > 0 && d.Percent >= 99:
			complete++
		case d.VillagesInCircles > 0:
			started++
		}
	}
	if complete > 0 || started > 0 {
		out = append(out, fmt.Sprintf(
			"%d district(s) are fully mapped and %d are part-way. The mapping is "+
				"a district record and is loaded in the same format as the LGD "+
				"file; re-loading the base file afterwards does not undo it.",
			complete, started))
	}

	if len(out) == 0 {
		out = append(out, "Every village sits under a circle.")
	}
	return out
}

// commas groups an integer for reading. Indian grouping deliberately: a
// figure written 44,911 reads wrong to somebody expecting 44,911 as 44911.
func commas(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	// Last three, then twos.
	out := s[len(s)-3:]
	s = s[:len(s)-3]
	for len(s) > 2 {
		out = s[len(s)-2:] + "," + out
		s = s[:len(s)-2]
	}
	if s != "" {
		out = s + "," + out
	}
	return out
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
