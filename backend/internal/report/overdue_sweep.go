package report

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/notify"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Telling somebody a report has been waiting too long.
//
// # Who is told, and why it is not only the officer who is late
//
// Both: the officer holding the rung, and the rung above them.
//
// Telling only the officer who is late is what the platform already did, in
// effect, by showing them their own worklist. It fails in exactly the case
// that matters -- an officer who has stopped opening the console is precisely
// the officer whose reports are late, and nobody above them could see it.
//
// Telling only the supervisor would be worse in the ordinary case, where the
// officer is simply busy and a nudge is all that is needed.
//
// # Why nothing moves
//
// Nothing does. Being late puts a report in two bells and leaves it exactly
// where it is. A report that escalated on a timer would arrive at the next
// rung with no finding behind it, and would quietly reward the officer who
// ignored it -- see overdue.go, which has refused this since the ladder was
// built, and which the development plan disagrees with. The disagreement was
// settled in favour of flag-and-notify.

// aboveRung is the rung that supervises another. The District is the top; a
// report late there has nobody above it, and the notification goes only to
// the Collector's own bell.
func aboveRung(l Level) (rbac.Role, bool) {
	switch l {
	case LevelCircle:
		return rbac.RoleTehsildar, true
	case LevelTehsil:
		return rbac.RoleDistrictMagistrate, true
	}
	return "", false
}

// SweepOverdue notifies on every report that has passed its rung's allowance.
//
// Runs on a ticker. Re-running it does not spam: officer_notifications carries
// a unique index over (user, kind, resource) while unread, so the first
// warning is recorded and later sweeps record nothing until somebody has read
// and dismissed it -- at which point being told again is right, because the
// report is now later still.
func (s *Store) SweepOverdue(ctx context.Context) (int, error) {
	if s.officers == nil {
		return 0, nil
	}

	// Unscoped: this is the platform looking at every rung, not an officer
	// looking at their own. Recipients are resolved per report from the
	// village it belongs to.
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.reference, r.approval_level::text,
		       r.approval_level_since, v.id
		  FROM crop_loss_reports r
		  JOIN jurisdictions v
		    ON v.code = r.village_lgd_code AND v.level = 'village'
		 WHERE r.approval_level IS NOT NULL
		   AND r.approval_level <> 'village_pair'
		   AND r.state NOT IN ('adjudicated', 'closed', 'withdrawn')
		   AND r.approval_level_since IS NOT NULL
		   AND now() > r.approval_level_since + CASE r.approval_level
		            WHEN 'circle'   THEN $1::interval
		            WHEN 'tehsil'   THEN $2::interval
		            WHEN 'district' THEN $3::interval
		       END`,
		CircleSLA.String(), TehsilSLA.String(), DistrictSLA.String())
	if err != nil {
		return 0, fmt.Errorf("finding overdue reports: %w", err)
	}
	defer rows.Close()

	type late struct {
		id, ref, level, villageID string
		since                     time.Time
	}
	var all []late
	for rows.Next() {
		var l late
		if err := rows.Scan(&l.id, &l.ref, &l.level, &l.since, &l.villageID); err != nil {
			return 0, err
		}
		all = append(all, l)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	n := 0
	for _, l := range all {
		rung := Level(l.level)
		days := int(time.Since(l.since).Hours() / 24)

		roles := []rbac.Role{roleAt(rung)}
		if above, ok := aboveRung(rung); ok {
			roles = append(roles, above)
		}

		to, err := s.officers.Recipients(ctx, s.pool, l.villageID, roles)
		if err != nil {
			return n, err
		}
		if len(to) == 0 {
			continue // nobody holds the rung; the account roll is the problem
		}

		if err := s.officers.RecordMany(ctx, s.pool, to, notify.New{
			Kind: notify.KindReportOverdue,
			Title: fmt.Sprintf("%s has been waiting %d days at %s",
				l.ref, days, rungName[rung]),
			Body: "It is past the time this level is allowed. It has not moved " +
				"and will not move on its own.",
			Link:         linkFor(rung),
			ResourceType: "crop_loss_report",
			ResourceID:   l.id,
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
