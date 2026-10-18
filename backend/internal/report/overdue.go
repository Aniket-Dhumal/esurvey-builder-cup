package report

import (
	"context"
	"fmt"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Verifications that have run past their deadline, for the officer above them.
//
// # Why this is separate from the worklist
//
// Worklist filters on a.officer_id, so the seven-day deadline is visible only
// to the person breaching it. An officer who has stopped opening the console
// is precisely the officer whose reports are late, and nobody above them can
// see it -- a Tehsildar had no way to learn that a village's claims had been
// sitting for a fortnight.
//
// # Why nothing here decides anything
//
// It flags and it names. No report escalates or reassigns itself on a timer:
// a figure nobody looked at is exactly what this system exists to prevent,
// and an automatic escalation would hand a supervisor a claim with no field
// finding behind it while rewarding the officer who ignored it.

// OverdueEntry is one assignment past its deadline.
type OverdueEntry struct {
	ReportID  string `json:"report_id"`
	Reference string `json:"reference"`

	FarmerName  string `json:"farmer_name"`
	VillageName string `json:"village_name"`
	VillageCode string `json:"village_lgd_code"`

	OfficerID   string    `json:"officer_id"`
	OfficerName string    `json:"officer_name"`
	Role        rbac.Role `json:"role"`

	Tier       Tier      `json:"tier"`
	AssignedAt time.Time `json:"assigned_at"`
	DueAt      time.Time `json:"due_at"`

	// DaysOverdue is what a supervisor sorts by. Whole days, because "3.4
	// days late" is precision nobody acts on differently.
	DaysOverdue int `json:"days_overdue"`

	// State is the report's, so a supervisor can tell a claim waiting on its
	// first finding from one waiting on its second.
	State State `json:"state"`
}

// Overdue lists assignments past their deadline inside a scope.
//
// Ordered by how late they are: the fortnight-old claim is the one somebody
// has to answer for, and it should not be on page three behind yesterday's.
func (s *Store) Overdue(
	ctx context.Context, scope rbac.Scope, p page.Params,
) (page.Result[OverdueEntry], error) {
	if scope.Empty() {
		return page.Wrap([]OverdueEntry{}, 0, p), nil
	}

	jur := nullableUUIDs(scope.JurisdictionIDs)
	farmer := nullString(scope.FarmerID)
	search := ""
	if p.Query != "" {
		search = page.Like(p.Query)
	}

	// Superseded assignments are excluded: a reassigned row is not late, it
	// is replaced, and counting it would show a supervisor a breach that
	// somebody already dealt with.
	const where = `a.completed_at IS NULL
		  AND a.superseded_at IS NULL
		  AND a.due_at < now()
		  AND r.state NOT IN ('adjudicated', 'closed', 'withdrawn')
		  AND ($3::text = '' OR r.reference ILIKE $3
		                     OR r.farmer_name ILIKE $3
		                     OR r.village_name ILIKE $3
		                     OR u.full_name ILIKE $3)`

	const from = `
		FROM report_assignments a
		JOIN crop_loss_reports r ON r.id = a.report_id
		JOIN users u ON u.id = a.officer_id`

	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*)`+from+`
		WHERE `+scopePredicate("$1", "$2")+` AND `+where,
		jur, farmer, search).Scan(&total); err != nil {
		return page.Result[OverdueEntry]{}, fmt.Errorf("counting overdue verifications: %w", err)
	}
	if total == 0 {
		return page.Wrap([]OverdueEntry{}, 0, p), nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.reference, r.farmer_name,
		       r.village_name, r.village_lgd_code,
		       a.officer_id, u.full_name, a.role::text,
		       a.tier, a.assigned_at, a.due_at, r.state
		`+from+`
		WHERE `+scopePredicate("$1", "$2")+` AND `+where+`
		ORDER BY a.due_at, r.reference
		LIMIT $4 OFFSET $5`,
		jur, farmer, search, p.Limit, p.Offset)
	if err != nil {
		return page.Result[OverdueEntry]{}, fmt.Errorf("reading overdue verifications: %w", err)
	}
	defer rows.Close()

	now := time.Now()
	out := make([]OverdueEntry, 0, p.Limit)
	for rows.Next() {
		var e OverdueEntry
		var role, state string
		if err := rows.Scan(
			&e.ReportID, &e.Reference, &e.FarmerName,
			&e.VillageName, &e.VillageCode,
			&e.OfficerID, &e.OfficerName, &role,
			&e.Tier, &e.AssignedAt, &e.DueAt, &state,
		); err != nil {
			return page.Result[OverdueEntry]{}, err
		}
		e.Role, e.State = rbac.Role(role), State(state)
		e.DaysOverdue = int(now.Sub(e.DueAt).Hours() / 24)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return page.Result[OverdueEntry]{}, err
	}

	return page.Wrap(out, total, p), nil
}
