package report

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Database-backed, because every property here is a property of the trigger
// and the partial unique index. Above the pool they are all invisible: the Go
// code issues an UPDATE and cannot tell whether Postgres honoured it until
// Postgres is there to refuse.
//
// This is the gap that hid the defect these tests were written for. The return
// path has issued `UPDATE report_verifications SET superseded_at = now()`
// since migration 0023, against a table that migration 0006 had made refuse
// every UPDATE. It could not ever have worked, and nothing failed.

// seedPair is one report with the pair's findings and nothing above them --
// the state an amendment is legal in.
func seedPair(t *testing.T, s *Store, pool *pgxpool.Pool) fixture {
	t.Helper()
	f := seedAdjudicated(t, s, pool)

	// Undo the adjudication the shared fixture performs. Test data, so the
	// trigger is lifted deliberately rather than worked around.
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `ALTER TABLE report_adjudications DISABLE TRIGGER USER`)
	if _, err := pool.Exec(ctx,
		`DELETE FROM report_adjudications WHERE report_id = $1`, f.reportID); err != nil {
		t.Fatalf("clearing the adjudication: %v", err)
	}
	_, _ = pool.Exec(ctx, `ALTER TABLE report_adjudications ENABLE TRIGGER USER`)
	if _, err := pool.Exec(ctx, `
		UPDATE crop_loss_reports
		   SET state = 'verified', approval_level = 'village_pair',
		       approval_level_since = now(),
		       adjudicated_percent = NULL, adjudicated_area_ha = NULL,
		       adjudicated_at = NULL
		 WHERE id = $1`, f.reportID); err != nil {
		t.Fatalf("returning the report to the pair: %v", err)
	}
	return f
}

func standingPercent(t *testing.T, s *Store, reportID, officerID string) *float64 {
	t.Helper()
	findings, err := s.Findings(context.Background(), reportID)
	if err != nil {
		t.Fatalf("reading findings: %v", err)
	}
	for _, f := range findings {
		if f.OfficerID == officerID {
			return f.DamagePercent
		}
	}
	return nil
}

// The trigger's whole reason for existing.
func TestAStandingFindingCannotBeEdited(t *testing.T) {
	s, pool := testStore(t)
	f := seedPair(t, s, pool)

	_, err := pool.Exec(context.Background(), `
		UPDATE report_verifications SET damage_percent = 99
		 WHERE report_id = $1 AND officer_id = $2`, f.reportID, f.talathi)
	if err == nil {
		t.Fatal("a recorded finding was edited in place")
	}
	if got := standingPercent(t, s, f.reportID, f.talathi); got == nil || *got != 60 {
		t.Fatalf("the figure moved: %v", got)
	}
}

func TestAStandingFindingCannotBeDeleted(t *testing.T) {
	s, pool := testStore(t)
	f := seedPair(t, s, pool)

	if _, err := pool.Exec(context.Background(),
		`DELETE FROM report_verifications WHERE report_id = $1`, f.reportID); err == nil {
		t.Fatal("a recorded finding was deleted")
	}
}

// The narrowed exemption: the supersede stamp, and nothing else with it.
func TestSupersedingIsAllowedAndOnlySuperseding(t *testing.T) {
	s, pool := testStore(t)
	f := seedPair(t, s, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		UPDATE report_verifications
		   SET superseded_at = now(), superseded_reason = 'returned'
		 WHERE report_id = $1 AND officer_id = $2`, f.reportID, f.talathi); err != nil {
		t.Fatalf("superseding a finding was refused: %v", err)
	}

	// Not a back door. A supersede that also moves the figure is the edit the
	// trigger exists to refuse, wearing a different hat.
	if _, err := pool.Exec(ctx, `
		UPDATE report_verifications
		   SET superseded_at = now(), damage_percent = 99
		 WHERE report_id = $1 AND officer_id = $2`, f.reportID, f.sahayak); err == nil {
		t.Fatal("a figure was changed under cover of superseding the row")
	}

	// And a superseded row cannot be brought back.
	if _, err := pool.Exec(ctx, `
		UPDATE report_verifications SET superseded_at = NULL
		 WHERE report_id = $1 AND officer_id = $2`, f.reportID, f.talathi); err == nil {
		t.Fatal("a superseded finding was reinstated")
	}
}

// The defect this migration was written for: the Circle sending a report back.
func TestReturningToTheVillagePairSupersedesTheirFindings(t *testing.T) {
	s, pool := testStore(t)
	f := seedPair(t, s, pool)
	ctx := context.Background()

	// Put it at the Circle, which is where a return comes from.
	if _, err := pool.Exec(ctx, `
		UPDATE crop_loss_reports SET approval_level = 'circle',
		       approval_level_since = now() WHERE id = $1`, f.reportID); err != nil {
		t.Fatalf("moving the report to the Circle: %v", err)
	}

	if _, err := s.Return(ctx, Decide{
		ReportID:  f.reportID,
		OfficerID: f.mandal,
		Role:      rbac.RoleMandalOfficer,
		Scope:     scopeOf(t, pool, "RV-T1"),
		Reason:    "The photographs do not show the parcel in the claim.",
	}); err != nil {
		t.Fatalf("returning the report: %v", err)
	}

	findings, err := s.Findings(ctx, f.reportID)
	if err != nil {
		t.Fatalf("reading findings: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("the pair's findings still stand after a return: %d", len(findings))
	}

	var reason string
	if err := pool.QueryRow(ctx, `
		SELECT DISTINCT superseded_reason FROM report_verifications
		 WHERE report_id = $1`, f.reportID).Scan(&reason); err != nil {
		t.Fatalf("reading the supersede reason: %v", err)
	}
	if reason != "returned" {
		t.Fatalf("superseded_reason = %q, want returned", reason)
	}
}

// The point of the whole change: two officers a few points apart close the gap
// themselves rather than sending it to the Circle as a disagreement.
func TestAnOfficerMayReviseAfterReadingTheOther(t *testing.T) {
	s, pool := testStore(t)
	f := seedPair(t, s, pool)
	ctx := context.Background()

	revised := 62.0
	got, err := s.AmendFinding(ctx, NewFinding{
		ReportID:      f.reportID,
		OfficerID:     f.talathi,
		Role:          rbac.RoleTalathi,
		Tier:          TierVillagePair,
		Outcome:       OutcomeConfirmed,
		DamagePercent: &revised,
		Findings:      "Recounted the north quadrat with the Krushi Sahayak; agreed at 62.",
	})
	if err != nil {
		t.Fatalf("amending: %v", err)
	}
	if !got.SawPeerFinding {
		t.Error("an amendment is by definition not blind and must say so")
	}
	if !got.Revised {
		t.Error("a revised finding must be distinguishable from one merely recorded second")
	}

	if p := standingPercent(t, s, f.reportID, f.talathi); p == nil || *p != 62 {
		t.Fatalf("the standing figure is %v, want 62", p)
	}

	// Both are kept, and the original is marked as an amendment rather than as
	// a return -- the officer was not sent back, they agreed.
	var reason string
	var kept int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), coalesce(max(superseded_reason), '')
		  FROM report_verifications
		 WHERE report_id = $1 AND officer_id = $2`,
		f.reportID, f.talathi).Scan(&kept, &reason); err != nil {
		t.Fatalf("counting the officer's findings: %v", err)
	}
	if kept != 2 {
		t.Fatalf("the original finding was not kept: %d rows", kept)
	}
	if reason != "amended" {
		t.Fatalf("superseded_reason = %q, want amended", reason)
	}

	c := Reconcile(mustFindings(t, s, f.reportID))
	if c.Divergence == nil || *c.Divergence != 0 {
		t.Fatalf("the pair converged and the divergence is %v", c.Divergence)
	}
}

// An amendment before the peer has filed is a rewrite of a blind finding.
func TestAnAmendmentIsRefusedBeforeThePeerHasRecorded(t *testing.T) {
	s, pool := testStore(t)
	f := seedPair(t, s, pool)
	ctx := context.Background()

	// Take the Krushi Sahayak's finding out, leaving the Talathi alone.
	if _, err := pool.Exec(ctx, `
		UPDATE report_verifications
		   SET superseded_at = now(), superseded_reason = 'returned'
		 WHERE report_id = $1 AND officer_id = $2`, f.reportID, f.sahayak); err != nil {
		t.Fatalf("superseding the peer: %v", err)
	}

	revised := 70.0
	_, err := s.AmendFinding(ctx, NewFinding{
		ReportID:      f.reportID,
		OfficerID:     f.talathi,
		Role:          rbac.RoleTalathi,
		Tier:          TierVillagePair,
		Outcome:       OutcomeConfirmed,
		DamagePercent: &revised,
		Findings:      "Looked again on my own and revised the figure upward.",
	})
	if !errors.Is(err, ErrPeerHasNotRecorded) {
		t.Fatalf("error = %v, want ErrPeerHasNotRecorded", err)
	}
}

// Once a supervisor holds it, amending underneath them changes what is being
// decided.
func TestTheAmendWindowClosesWhenTheReportClimbs(t *testing.T) {
	s, pool := testStore(t)
	f := seedPair(t, s, pool)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		UPDATE crop_loss_reports SET approval_level = 'circle' WHERE id = $1`,
		f.reportID); err != nil {
		t.Fatalf("moving the report up: %v", err)
	}

	revised := 62.0
	_, err := s.AmendFinding(ctx, NewFinding{
		ReportID:      f.reportID,
		OfficerID:     f.talathi,
		Role:          rbac.RoleTalathi,
		Tier:          TierVillagePair,
		Outcome:       OutcomeConfirmed,
		DamagePercent: &revised,
		Findings:      "Agreed with the Krushi Sahayak after speaking to them.",
	})
	if !errors.Is(err, ErrAmendWindowClosed) {
		t.Fatalf("error = %v, want ErrAmendWindowClosed", err)
	}
}

func mustFindings(t *testing.T, s *Store, reportID string) []Finding {
	t.Helper()
	f, err := s.Findings(context.Background(), reportID)
	if err != nil {
		t.Fatalf("reading findings: %v", err)
	}
	return f
}

// scopeOf is the posting an officer acts from. The ladder refuses a decision
// from outside it, which is the point of taking a scope at all.
func scopeOf(t *testing.T, pool *pgxpool.Pool, code string) rbac.Scope {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM jurisdictions WHERE code = $1`, code).Scan(&id); err != nil {
		t.Fatalf("reading the jurisdiction %s: %v", code, err)
	}
	return rbac.Scope{JurisdictionIDs: []string{id}}
}
