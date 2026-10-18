package report

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Moving a report up and down the ladder.
//
// The rule itself lives in ladder.go as a pure function; this is the part that
// reads where a report stands, writes what an officer decided, and moves the
// report. Kept apart so the rule can be tested exhaustively without a database
// and this can be read as plumbing.

// Approval is one rung's decision, as recorded.
type Approval struct {
	ID       string `json:"id"`
	ReportID string `json:"report_id"`

	Level     Level     `json:"level"`
	OfficerID string    `json:"officer_id"`
	Officer   string    `json:"officer_name,omitempty"`
	Role      rbac.Role `json:"role"`

	Decision string `json:"decision"`

	PercentBefore *float64 `json:"percent_before,omitempty"`
	PercentAfter  *float64 `json:"percent_after,omitempty"`
	AreaBeforeHa  *float64 `json:"area_before_ha,omitempty"`
	AreaAfterHa   *float64 `json:"area_after_ha,omitempty"`

	Reason    string    `json:"reason,omitempty"`
	DecidedAt time.Time `json:"decided_at"`

	// Superseded means a level above sent the report back, so this approval no
	// longer stands. Kept because what an officer passed before being asked to
	// look again is what a dispute turns on.
	SupersededAt *time.Time `json:"superseded_at,omitempty"`
}

// Decisions.
const (
	DecisionApproved = "approved"
	DecisionReturned = "returned"
)

// Decide is one officer acting on one report.
type Decide struct {
	ReportID  string
	OfficerID string
	Role      rbac.Role

	// Scope is the jurisdiction the officer is posted to.
	//
	// Required, and enforced here rather than in the handler. The ladder's
	// order stops a Tehsildar approving before the Circle; nothing in it stops
	// the Tehsildar of Barshi approving a report in Mohol, which is a
	// different officer signing for a village they have never seen. A check in
	// the handler is one new endpoint away from being forgotten; a check in
	// the query cannot be.
	Scope rbac.Scope

	// Percent and AreaHa are the figures the officer is passing upward. Nil
	// keeps what they were shown, which is the ordinary case: most approvals
	// change nothing.
	Percent *float64
	AreaHa  *float64

	Reason string

	// Bulk marks this as one item of a batch rather than a report somebody
	// opened and read.
	Bulk bool
}

// standing reads what the ladder needs to judge a move, and the figure the
// officer is acting on.
func (s *Store) standing(ctx context.Context, tx pgx.Tx, reportID string, scope rbac.Scope) (
	Standing, *float64, *float64, error,
) {
	var (
		st        Standing
		level     *string
		state     string
		pct, area *float64
	)

	// Locked and scoped in one statement. FOR UPDATE so two officers pressing
	// approve at the same moment cannot both read the report as waiting at
	// their rung; the scope predicate so a report outside the officer's
	// posting is not there to be locked in the first place.
	err := tx.QueryRow(ctx, `
		SELECT r.approval_level::text, r.contested, r.state,
		       r.adjudicated_percent, r.adjudicated_area_ha
		  FROM crop_loss_reports r
		 WHERE r.id = $1 AND `+scopePredicate("$2", "$3")+`
		 FOR UPDATE OF r`,
		reportID, nullableUUIDs(scope.JurisdictionIDs), nullString(scope.FarmerID)).
		Scan(&level, &st.Contested, &state, &pct, &area)
	if errors.Is(err, pgx.ErrNoRows) {
		// Out of scope and non-existent answer identically, so a run of report
		// ids cannot be turned into a map of which villages have filed.
		return st, nil, nil, ErrNotFound
	}
	if err != nil {
		return st, nil, nil, err
	}

	if level != nil {
		st.At = Level(*level)
	}
	// A report that has been adjudicated or closed has finished climbing.
	st.Closed = state == string(StateClosed) || state == string(StateAdjudicated) ||
		state == string(StateWithdrawn)

	// What the village pair actually recorded, read once and used twice: to
	// establish that they finished, and as the figure to fall back on.
	//
	// Read unconditionally rather than only when no rung has approved yet.
	// The completeness question is asked of every move up the ladder, and a
	// report that carries a rung is precisely the one whose findings nobody
	// was re-checking.
	findings, ferr := s.findingsTx(ctx, tx, reportID)
	if ferr != nil {
		return st, nil, nil, ferr
	}
	consensus := Reconcile(findings)
	st.PairComplete = consensus.Complete

	// The figure this officer is acting on: whatever the last standing rung
	// passed up, and failing that the village pair's own reconciliation.
	var apct, aarea *float64
	err = tx.QueryRow(ctx, `
		SELECT percent_after, area_after_ha
		  FROM report_approvals
		 WHERE report_id = $1 AND decision = 'approved' AND superseded_at IS NULL
		 ORDER BY decided_at DESC LIMIT 1`, reportID).Scan(&apct, &aarea)
	switch {
	case err == nil:
		if apct != nil {
			pct = apct
		}
		if aarea != nil {
			area = aarea
		}
	case errors.Is(err, pgx.ErrNoRows):
		if consensus.Mean != nil {
			m := *consensus.Mean
			pct = &m
		}
	default:
		return st, nil, nil, err
	}

	return st, pct, area, nil
}

// Approve records one level's sign-off and moves the report up.
//
// The District's approval is the last: it writes the adjudicated figure onto
// the report, because at that point the number has stopped changing and every
// reader downstream — the dossier, the payment file, the farmer's copy — wants
// one field rather than a walk back through four rungs.
func (s *Store) Approve(ctx context.Context, in Decide) (*Approval, error) {
	return s.decide(ctx, in, DecisionApproved)
}

// Return sends a report back one rung, with a reason.
//
// One rung, not to the bottom. An officer who merely disagrees with the figure
// can change it; returning means the evidence itself will not carry the claim,
// and the level immediately below is the one that has to answer for that.
// Dropping it straight to the village pair from the District would also erase
// two levels of sign-off that were not the problem.
func (s *Store) Return(ctx context.Context, in Decide) (*Approval, error) {
	return s.decide(ctx, in, DecisionReturned)
}

func (s *Store) decide(ctx context.Context, in Decide, decision string) (*Approval, error) {
	rung, ok := LevelFor(in.Role)
	if !ok {
		return nil, ErrNoRung
	}
	// A principal with no posting reaches nothing. Checked before the
	// transaction so an unposted account cannot even take a row lock.
	if in.Scope.Empty() {
		return nil, ErrNotFound
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	st, pct, area, err := s.standing(ctx, tx, in.ReportID, in.Scope)
	if err != nil {
		return nil, err
	}

	after, afterArea := pct, area
	changed := false
	if decision == DecisionApproved {
		if in.Percent != nil {
			if *in.Percent < 0 || *in.Percent > 100 {
				return nil, fmt.Errorf("%w: the figure is out of range", ErrBadInput)
			}
			changed = pct == nil || *in.Percent != *pct
			after = in.Percent
		}
		if in.AreaHa != nil {
			if *in.AreaHa < 0 {
				return nil, fmt.Errorf("%w: the area is negative", ErrBadInput)
			}
			changed = changed || area == nil || *in.AreaHa != *area
			afterArea = in.AreaHa
		}
	}

	if err := Check(st, Move{
		Role: in.Role, Changed: changed, Reason: in.Reason, Bulk: in.Bulk,
	}); err != nil {
		return nil, err
	}

	var row Approval
	if decision == DecisionReturned {
		row, err = s.recordReturn(ctx, tx, in, rung)
	} else {
		row, err = s.recordApproval(ctx, tx, in, rung, pct, area, after, afterArea)
	}
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *Store) recordApproval(
	ctx context.Context, tx pgx.Tx, in Decide, rung Level,
	before, beforeArea, after, afterArea *float64,
) (Approval, error) {
	var row Approval
	err := tx.QueryRow(ctx, `
		INSERT INTO report_approvals
			(report_id, level, officer_id, role, decision,
			 percent_before, percent_after, area_before_ha, area_after_ha, reason)
		VALUES ($1, $2::approval_level, $3, $4::officer_role, 'approved',
		        $5, $6, $7, $8, $9)
		RETURNING id, decided_at`,
		in.ReportID, string(rung), in.OfficerID, string(in.Role),
		before, after, beforeArea, afterArea, nullString(in.Reason)).
		Scan(&row.ID, &row.DecidedAt)
	if err != nil {
		return row, fmt.Errorf("recording the approval: %w", err)
	}

	row = Approval{
		ID: row.ID, ReportID: in.ReportID, Level: rung,
		OfficerID: in.OfficerID, Role: in.Role, Decision: DecisionApproved,
		PercentBefore: before, PercentAfter: after,
		AreaBeforeHa: beforeArea, AreaAfterHa: afterArea,
		Reason: in.Reason, DecidedAt: row.DecidedAt,
	}

	// Settling ends it.
	//
	// There is nothing above to pass it to: the claim reached this officer
	// because the village pair disagreed, and one officer was asked to answer.
	// It used to climb from here to the Tehsil and then the District, each
	// approving in turn; see the head of ladder.go for why it no longer does.
	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET state = 'adjudicated',
		       adjudicated_percent = COALESCE($2, adjudicated_percent),
		       adjudicated_area_ha = COALESCE($3, adjudicated_area_ha),
		       adjudicated_at = now(),
		       updated_at = now()
		 WHERE id = $1`, in.ReportID, after, afterArea); err != nil {
		return row, err
	}
	return row, nil
}

func (s *Store) recordReturn(
	ctx context.Context, tx pgx.Tx, in Decide, rung Level,
) (Approval, error) {
	var row Approval

	// One rung down. The ladder guarantees rung is not the village pair, so
	// there is always something below.
	var below Level
	for i, l := range ladder {
		if l == rung && i > 0 {
			below = ladder[i-1]
		}
	}
	if below == "" {
		return row, fmt.Errorf("%w: there is no level below %s", ErrBadInput, rung)
	}

	err := tx.QueryRow(ctx, `
		INSERT INTO report_approvals
			(report_id, level, officer_id, role, decision, reason)
		VALUES ($1, $2::approval_level, $3, $4::officer_role, 'returned', $5)
		RETURNING id, decided_at`,
		in.ReportID, string(rung), in.OfficerID, string(in.Role), in.Reason).
		Scan(&row.ID, &row.DecidedAt)
	if err != nil {
		return row, fmt.Errorf("recording the return: %w", err)
	}

	// Whatever stands at the rung below no longer does, or that level could
	// not act again — and the unique index would refuse it if it tried.
	if below == LevelVillagePair {
		// The pair have to look again, so their findings are superseded and
		// the report goes back to being unverified.
		if _, err := tx.Exec(ctx, `
			UPDATE report_verifications
			   SET superseded_at = now(), superseded_reason = 'returned'
			 WHERE report_id = $1 AND tier = 'village_pair' AND superseded_at IS NULL`,
			in.ReportID); err != nil {
			return row, err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE crop_loss_reports SET state = 'under_verification'
			 WHERE id = $1 AND state NOT IN ('closed', 'withdrawn')`,
			in.ReportID); err != nil {
			return row, err
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE report_approvals
		   SET superseded_at = now(), superseded_by = $3
		 WHERE report_id = $1 AND level = $2::approval_level
		   AND decision = 'approved' AND superseded_at IS NULL`,
		in.ReportID, string(below), row.ID); err != nil {
		return row, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET approval_level = $2::approval_level,
		       approval_level_since = now(),
		       returned_count = returned_count + 1,
		       updated_at = now()
		 WHERE id = $1`, in.ReportID, string(below)); err != nil {
		return row, err
	}

	// The rung it went back to. A different notification kind from an
	// arrival on purpose: this is a report an officer has already looked at
	// and is being told to look at again.
	if err := s.returned(ctx, tx, in.ReportID, below, in.Reason); err != nil {
		s.noticeFailed("announcing a return", in.ReportID, err)
	}

	row = Approval{
		ID: row.ID, ReportID: in.ReportID, Level: rung,
		OfficerID: in.OfficerID, Role: in.Role, Decision: DecisionReturned,
		Reason: in.Reason, DecidedAt: row.DecidedAt,
	}
	return row, nil
}

// BulkOutcome is what happened to one report in a batch.
type BulkOutcome struct {
	ReportID string `json:"report_id"`
	Approved bool   `json:"approved"`
	// Reason names why it was refused, in words the officer can act on.
	Reason string `json:"reason,omitempty"`
}

// BulkApprove approves many reports and reports on each one separately.
//
// # Why each item is its own transaction
//
// A batch of two hundred where one is contested must approve the other
// hundred and ninety-nine. Rolling the lot back because one report needed
// reading would make the feature useless exactly when it is most wanted, which
// is a district-wide calamity.
//
// # Why it cannot skip anything
//
// Every item goes through the same Approve path as a single report, so the
// ladder's order, the jurisdiction and the contested rule all apply unchanged.
// Bulk is a saving of clicks, never of scrutiny: a report the level below has
// not passed is refused inside the batch and says so.
func (s *Store) BulkApprove(ctx context.Context, ids []string, in Decide) ([]BulkOutcome, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%w: no reports were selected", ErrBadInput)
	}
	if len(ids) > MaxBulkApproval {
		return nil, fmt.Errorf(
			"%w: %d reports at once is more than the %d this allows; narrow the filter",
			ErrBadInput, len(ids), MaxBulkApproval)
	}

	out := make([]BulkOutcome, 0, len(ids))
	for _, id := range ids {
		item := in
		item.ReportID = id
		item.Bulk = true
		// A bulk approval passes the figure through untouched. Changing two
		// hundred percentages to the same number in one click is not a
		// judgement anybody made about any of those fields.
		item.Percent, item.AreaHa = nil, nil

		if _, err := s.Approve(ctx, item); err != nil {
			out = append(out, BulkOutcome{ReportID: id, Reason: err.Error()})
			continue
		}
		out = append(out, BulkOutcome{ReportID: id, Approved: true})
	}
	return out, nil
}

// MaxBulkApproval bounds one batch.
//
// Not a performance limit. It is how many reports an officer can be said to
// have decided in one action before the claim becomes untrue, and it bounds
// the damage a mistaken filter can do before somebody notices.
const MaxBulkApproval = 200

// Approvals returns a report's ladder history, oldest first.
func (s *Store) Approvals(ctx context.Context, reportID string) ([]Approval, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.report_id, a.level::text, a.officer_id,
		       COALESCE(u.full_name, ''), a.role::text, a.decision::text,
		       a.percent_before, a.percent_after, a.area_before_ha, a.area_after_ha,
		       COALESCE(a.reason, ''), a.decided_at, a.superseded_at
		  FROM report_approvals a
		  LEFT JOIN users u ON u.id = a.officer_id
		 WHERE a.report_id = $1
		 ORDER BY a.decided_at`, reportID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Approval{}
	for rows.Next() {
		var a Approval
		var lvl, role, dec string
		if err := rows.Scan(&a.ID, &a.ReportID, &lvl, &a.OfficerID, &a.Officer,
			&role, &dec, &a.PercentBefore, &a.PercentAfter,
			&a.AreaBeforeHa, &a.AreaAfterHa, &a.Reason, &a.DecidedAt,
			&a.SupersededAt); err != nil {
			return nil, err
		}
		a.Level, a.Role, a.Decision = Level(lvl), rbac.Role(role), dec
		out = append(out, a)
	}
	return out, rows.Err()
}
