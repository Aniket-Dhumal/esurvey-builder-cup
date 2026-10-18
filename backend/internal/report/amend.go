package report

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Letting the village pair converge on a figure.
//
// # The problem this solves
//
// The pair record independently and blind, which is right: two officers who
// arrive at the same number without conferring have said something a single
// officer cannot. But independence was the whole of it. Once both had filed,
// each could read the other's finding and neither could do anything with it.
// Two officers four points apart stayed four points apart on the record, and
// two officers eight points apart -- the same field, the same crop, one of
// them having counted a different quadrat -- sent the report to the Circle as
// a disagreement to be arbitrated.
//
// That is a great deal of a Circle Officer's week spent on gaps that the two
// people who actually stood in the field could close in a phone call. It was
// raised in exactly those terms: a single number should not cause an
// escalation.
//
// # What changes, and what deliberately does not
//
// The first finding is still blind, and still cannot be edited. What is added
// is a second act: after both have filed and each has read the other, either
// may record a revised finding, which supersedes their own and keeps it.
//
// The blindness is not weakened by this, because the original is preserved and
// marked. A reader can always see what each officer said before they knew what
// their colleague thought -- which is the evidence that matters in a dispute --
// and can see, separately, what they said afterwards.
//
// # Why an amendment is refused before the peer has filed
//
// Because then it is not an amendment, it is a rewrite of a blind finding, and
// there would be nothing to distinguish an officer correcting a typo from an
// officer who saw the report on a screen somewhere and adjusted. The database
// refuses it too: the append-only trigger allows exactly one transition, and
// nothing that would let a standing finding change in place.
//
// # Why it stops when the report leaves the pair
//
// Once the Circle holds the report, the pair's findings are the evidence the
// Circle is weighing. An officer amending underneath a supervisor who is
// mid-decision changes the thing being decided. If the pair need another look
// after that, the mechanism is the Circle returning the report, which is
// recorded as a return and counted as one.

// ErrNothingToAmend is returned when the officer has no standing finding.
var ErrNothingToAmend = errors.New("no finding to amend")

// ErrPeerHasNotRecorded is returned when the other authority has not filed.
var ErrPeerHasNotRecorded = errors.New("the other authority has not recorded yet")

// ErrAmendWindowClosed is returned once the report has left the village pair.
var ErrAmendWindowClosed = errors.New("the report has left the village pair")

// AmendFinding replaces an officer's own standing finding with a revised one.
//
// The old row is kept and marked amended -- not returned, which means
// something different about the officer and must not be counted with it.
func (s *Store) AmendFinding(ctx context.Context, in NewFinding) (*Finding, error) {
	if in.Tier != TierVillagePair {
		return nil, fmt.Errorf(
			"%w: only the village pair amend; a level above records afresh",
			ErrBadInput)
	}
	if err := validFinding(in); err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	// The window. Locked, so that a Circle Officer approving at this instant
	// and an officer amending at this instant cannot both succeed.
	var state string
	var level *string
	if err := tx.QueryRow(ctx, `
		SELECT state::text, approval_level::text
		  FROM crop_loss_reports WHERE id = $1 FOR UPDATE`,
		in.ReportID).Scan(&state, &level); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if level != nil && *level != string(LevelVillagePair) {
		return nil, fmt.Errorf("%w: it is with the %s", ErrAmendWindowClosed, *level)
	}
	switch state {
	case "adjudicated", "closed", "withdrawn":
		return nil, fmt.Errorf("%w: it is %s", ErrAmendWindowClosed, state)
	}

	// Their own standing finding, and the peer's.
	var mine string
	if err := tx.QueryRow(ctx, `
		SELECT id FROM report_verifications
		 WHERE report_id = $1 AND officer_id = $2 AND tier = $3
		   AND superseded_at IS NULL`,
		in.ReportID, in.OfficerID, string(in.Tier)).Scan(&mine); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNothingToAmend
		}
		return nil, err
	}

	var peers int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM report_verifications
		 WHERE report_id = $1 AND tier = $2 AND officer_id <> $3
		   AND superseded_at IS NULL`,
		in.ReportID, string(in.Tier), in.OfficerID).Scan(&peers); err != nil {
		return nil, err
	}
	if peers == 0 {
		return nil, ErrPeerHasNotRecorded
	}

	if _, err := tx.Exec(ctx, `
		UPDATE report_verifications
		   SET superseded_at = now(), superseded_reason = 'amended'
		 WHERE id = $1`, mine); err != nil {
		return nil, fmt.Errorf("superseding the earlier finding: %w", err)
	}

	// saw_peer_finding is true by construction: the amendment is only
	// reachable once the peer has filed, and the officer has read it.
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO report_verifications (
			report_id, officer_id, role, tier, outcome,
			damage_percent, area_affected_ha, findings, visited_at,
			saw_peer_finding, saw_model_assessment
		) VALUES ($1, $2, $3::officer_role, $4, $5, $6, $7, $8, $9, true, $10)
		RETURNING id`,
		in.ReportID, in.OfficerID, string(in.Role), string(in.Tier),
		string(in.Outcome), in.DamagePercent, in.AreaAffectedHa, in.Findings,
		in.VisitedAt, in.SawModelAssessment).Scan(&id); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadInput, cleanErr(err))
	}

	for _, it := range in.Items {
		if _, err := tx.Exec(ctx, `
			INSERT INTO report_verification_items (
				verification_id, line_no, crop_name,
				area_sown_ha, area_affected_ha, damage_percent
			) VALUES ($1, $2, NULLIF($3, ''), $4, $5, $6)`,
			id, it.LineNo, it.CropName,
			it.AreaSownHa, it.AreaAffectedHa, it.DamagePercent); err != nil {
			return nil, fmt.Errorf("%w: line %d: %s", ErrBadInput, it.LineNo, cleanErr(err))
		}
	}

	// The divergence is recomputed, because closing the gap is the point.
	// A pair who were contested and are no longer must stop being flagged as
	// contested -- otherwise the flag says "these two disagreed once", which
	// is not what any screen reading it means by the word.
	findings, err := s.findingsTx(ctx, tx, in.ReportID)
	if err != nil {
		return nil, err
	}
	c := Reconcile(findings)
	if _, err := tx.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET contested = $2, updated_at = now()
		 WHERE id = $1`, in.ReportID, contestedBy(c)); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	standing, err := s.Findings(ctx, in.ReportID)
	if err != nil {
		return nil, err
	}
	for i := range standing {
		if standing[i].ID == id {
			return &standing[i], nil
		}
	}
	return nil, ErrNotFound
}
