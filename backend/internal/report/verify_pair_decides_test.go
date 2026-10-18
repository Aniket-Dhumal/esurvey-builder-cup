package report

import (
	"context"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The village pair decide, and a disagreement goes to one officer.
//
// This is the whole of the approval flow as of 2026-09-28: nothing above the
// village approves a claim the pair agreed on, and a claim they did not agree
// on is settled by the Circle Officer, or by the Tehsildar where no Circle
// Officer is posted. See the head of ladder.go for what that replaced.

func TestAgreeingPairDecidesTheClaimWithNobodyAbove(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()
	id := aClaimWithTheOfficers(t, s, pool, "pair-0000000001")

	// Within the consensus band: 60 and 62.
	for _, rec := range []struct {
		officer string
		role    rbac.Role
		percent float64
	}{
		{f.talathi, rbac.RoleTalathi, 60},
		{f.sahayak, rbac.RoleKrushiSahayak, 62},
	} {
		if _, err := s.RecordFinding(ctx, NewFinding{
			ReportID: id, OfficerID: rec.officer, Role: rec.role,
			Tier: TierVillagePair, Outcome: OutcomeConfirmed,
			DamagePercent: ptr(rec.percent),
			Findings:      "standing water across the eastern half",
		}); err != nil {
			t.Fatalf("recording %s: %v", rec.role, err)
		}
	}

	state, _ := stateOf(t, pool, id)
	if state != string(StateAdjudicated) {
		t.Fatalf("the claim is at %q; an agreeing pair decides it", state)
	}

	var percent *float64
	var level *string
	if err := pool.QueryRow(ctx, `
		SELECT adjudicated_percent, approval_level::text
		  FROM crop_loss_reports WHERE id = $1`, id).Scan(&percent, &level); err != nil {
		t.Fatal(err)
	}
	if percent == nil {
		t.Fatal("the claim was decided and carries no figure")
	}
	// Their mean. Picking either officer's number would be picking a person.
	if *percent != 61 {
		t.Errorf("decided at %v%%, want the pair's mean of 61", *percent)
	}
	if level != nil {
		t.Errorf("the claim is waiting at %q; nothing above the village "+
			"approves an agreed claim", *level)
	}
}

func TestDisagreeingPairSendsTheClaimToOneOfficerToSettle(t *testing.T) {
	s, pool := testStore(t)
	f := seedAdjudicated(t, s, pool)
	ctx := context.Background()
	id := aClaimWithTheOfficers(t, s, pool, "pair-0000000002")

	// Far apart: 20 and 75.
	for _, rec := range []struct {
		officer string
		role    rbac.Role
		percent float64
	}{
		{f.talathi, rbac.RoleTalathi, 20},
		{f.sahayak, rbac.RoleKrushiSahayak, 75},
	} {
		if _, err := s.RecordFinding(ctx, NewFinding{
			ReportID: id, OfficerID: rec.officer, Role: rec.role,
			Tier: TierVillagePair, Outcome: OutcomeConfirmed,
			DamagePercent: ptr(rec.percent),
			Findings:      "what I saw",
		}); err != nil {
			t.Fatalf("recording %s: %v", rec.role, err)
		}
	}

	var level *string
	var contested bool
	var percent *float64
	if err := pool.QueryRow(ctx, `
		SELECT approval_level::text, contested, adjudicated_percent
		  FROM crop_loss_reports WHERE id = $1`, id).
		Scan(&level, &contested, &percent); err != nil {
		t.Fatal(err)
	}
	if !contested {
		t.Error("a 55-point gap was not marked contested")
	}
	if percent != nil {
		t.Errorf("a disagreed claim was decided at %v%% with nobody settling it",
			*percent)
	}
	if level == nil {
		t.Fatal("a disagreed claim was not routed to anybody")
	}
	// The Circle where one is posted, the Tehsil where none is. Either is
	// correct; what must not happen is that it goes nowhere.
	if *level != string(LevelCircle) && *level != string(LevelTehsil) {
		t.Errorf("routed to %q, want the circle or the tehsil", *level)
	}
}
