package report

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// How long each rung of the ladder has.
//
// # Why the upper rungs get less than the village pair
//
// The pair's seven days buy a journey. Somebody has to reach the field, walk
// it with the farmer, and record what they saw, around whatever else the
// village needs that week.
//
// Nobody above them goes anywhere. A Circle Officer, a Tehsildar and a
// Collector are reading a finding that is already written, against evidence
// already gathered, at a desk. Giving a desk review the same allowance as a
// field visit is how a claim that took a week to verify takes a month to
// approve -- and the whole ladder is four of those in a row.
//
// # Why the District gets longest of the three
//
// It is the last rung, it writes the figure that gets paid, and it is one
// officer for a district of claims. The others hand the work upward; the
// Collector is where it stops.
//
// These are defaults, not law. A district that works to different numbers
// should be able to say so, and that is a settings screen rather than a
// recompile -- see docs/REMAINING_WORK.md.
const (
	// CircleSLA is the Circle Officer's allowance. Shortest: they are nearest
	// the evidence and most often the officer who arbitrates a disagreement
	// while it is still fresh.
	CircleSLA = 3 * 24 * time.Hour

	// TehsilSLA is the Tehsildar's.
	TehsilSLA = 4 * 24 * time.Hour

	// DistrictSLA is kept for claims left at the district rung by the build
	// that had one. Nothing is routed there now; see ladder.go.
	DistrictSLA = 5 * 24 * time.Hour
)

// SLAFor is how long a rung has to answer. The village pair's deadline is
// per-officer and lives on their assignment row; see VerificationSLA.
func SLAFor(l Level) (time.Duration, bool) {
	switch l {
	case LevelCircle:
		return CircleSLA, true
	case LevelTehsil:
		return TehsilSLA, true
	}
	return 0, false
}

// WaitingEntry is one report that has been at a rung longer than that rung
// allows.
type WaitingEntry struct {
	ReportID  string `json:"report_id"`
	Reference string `json:"reference"`

	FarmerName  string `json:"farmer_name"`
	VillageName string `json:"village_name"`
	VillageCode string `json:"village_lgd_code"`

	// Level is the rung it is stuck at, and Role is who holds that rung.
	Level Level     `json:"level"`
	Role  rbac.Role `json:"role"`

	WaitingSince time.Time `json:"waiting_since"`
	DueAt        time.Time `json:"due_at"`
	DaysWaiting  int       `json:"days_waiting"`
	DaysOverdue  int       `json:"days_overdue"`

	// Contested reports were already harder; being late with one is worse.
	Contested bool `json:"contested"`
}

// OverdueRungs lists reports that have sat at a rung past its allowance.
//
// Distinct from Overdue in overdue.go, which is the village pair's per-officer
// deadline. Two questions with two shapes: there, a named officer is late;
// here, a rung is.
//
// # Why this is scoped to the caller and not to the rung
//
// An officer sees the reports late *inside their own posting*, which includes
// the rung they hold and every rung below it. A Tehsildar sees their own
// overdue reports and the Circle's; a Collector sees the district's. Being
// late is most usefully visible to the person above the delay, because the
// officer who has stopped opening the console is precisely the one whose
// reports are late.
//
// # Why nothing here moves a report
//
// Nothing does. This flags and it names, and the bell says the same thing.
// See the reasoning on SweepOverdue and in overdue.go: a report that escalates
// on a timer arrives at the next rung with no finding behind it, and rewards
// the officer who ignored it.
func (s *Store) OverdueRungs(ctx context.Context, scope rbac.Scope) ([]WaitingEntry, error) {
	if scope.Empty() {
		return []WaitingEntry{}, nil
	}

	// The allowance per rung, as SQL, so the comparison happens where the
	// rows are rather than by reading every waiting report into Go.
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.reference, r.farmer_name, r.village_name,
		       r.village_lgd_code, r.approval_level::text,
		       r.approval_level_since, r.contested
		  FROM crop_loss_reports r
		  JOIN jurisdictions v
		    ON v.code = r.village_lgd_code AND v.level = 'village'
		 WHERE r.approval_level IS NOT NULL
		   AND r.approval_level <> 'village_pair'
		   AND r.state NOT IN ('adjudicated', 'closed', 'withdrawn')
		   AND r.approval_level_since IS NOT NULL
		   AND now() > r.approval_level_since + CASE r.approval_level
		            WHEN 'circle'   THEN $2::interval
		            WHEN 'tehsil'   THEN $3::interval
		            WHEN 'district' THEN $4::interval
		       END
		   AND (v.id = ANY($1::uuid[]) OR v.ancestor_ids && $1::uuid[])
		 ORDER BY r.approval_level_since`,
		nullableUUIDs(scope.JurisdictionIDs),
		CircleSLA.String(), TehsilSLA.String(), DistrictSLA.String())
	if err != nil {
		return nil, fmt.Errorf("listing overdue reports: %w", err)
	}
	defer rows.Close()

	out := []WaitingEntry{}
	now := time.Now()
	for rows.Next() {
		var e WaitingEntry
		var level string
		if err := rows.Scan(&e.ReportID, &e.Reference, &e.FarmerName,
			&e.VillageName, &e.VillageCode, &level, &e.WaitingSince,
			&e.Contested); err != nil {
			return nil, err
		}
		e.Level = Level(level)
		allowance, _ := SLAFor(e.Level)
		e.DueAt = e.WaitingSince.Add(allowance)
		e.Role = roleAt(e.Level)
		e.DaysWaiting = int(now.Sub(e.WaitingSince).Hours() / 24)
		e.DaysOverdue = int(now.Sub(e.DueAt).Hours() / 24)
		out = append(out, e)
	}
	return out, rows.Err()
}

// roleAt is the officer who acts at a rung.
func roleAt(l Level) rbac.Role {
	switch l {
	case LevelCircle:
		return rbac.RoleMandalOfficer
	case LevelTehsil:
		return rbac.RoleTehsildar
	case LevelDistrict:
		return rbac.RoleDistrictMagistrate
	}
	return ""
}
