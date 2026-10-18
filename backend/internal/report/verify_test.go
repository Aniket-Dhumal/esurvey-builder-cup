package report

import (
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

func pairFinding(role rbac.Role, outcome Outcome, damage *float64) Finding {
	return Finding{
		OfficerID: string(role), Role: role, Tier: TierVillagePair,
		Outcome: outcome, DamagePercent: damage,
	}
}

func f(v float64) *float64 { return &v }

func talathi(damage float64) Finding {
	return pairFinding(rbac.RoleTalathi, OutcomeConfirmed, f(damage))
}

func krushi(damage float64) Finding {
	return pairFinding(rbac.RoleKrushiSahayak, OutcomeConfirmed, f(damage))
}

// Nothing is decided until both authorities have been.
func TestConsensusWaitsForBoth(t *testing.T) {
	for name, in := range map[string][]Finding{
		"none": {},
		"one":  {talathi(40)},
	} {
		got := Reconcile(in)
		if got.Complete {
			t.Errorf("%s: reported complete", name)
		}
		if got.Divergence != nil {
			t.Errorf("%s: produced a divergence from one finding", name)
		}
	}
}

func TestConsensusBands(t *testing.T) {
	cases := []struct {
		name      string
		a, b      float64
		wantRoute Route
		wantBy    rbac.Role
	}{
		{"identical", 40, 40, RouteConsensus, rbac.RoleMandalOfficer},
		{"within band", 40, 44, RouteConsensus, rbac.RoleMandalOfficer},
		{"exactly on the consensus line", 40, 45, RouteConsensus, rbac.RoleMandalOfficer},
		{"just past it", 40, 45.1, RouteArbitration, rbac.RoleMandalOfficer},
		{"mid arbitration", 30, 40, RouteArbitration, rbac.RoleMandalOfficer},
		{"exactly on the arbitration line", 30, 42, RouteArbitration, rbac.RoleMandalOfficer},
		{"past arbitration", 30, 42.1, RouteEscalation, rbac.RoleTehsildar},
		{"far apart", 10, 90, RouteEscalation, rbac.RoleTehsildar},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Reconcile([]Finding{talathi(c.a), krushi(c.b)})
			if !got.Complete {
				t.Fatal("not complete")
			}
			if got.Route != c.wantRoute {
				t.Errorf("route = %s, want %s (divergence %.2f)",
					got.Route, c.wantRoute, *got.Divergence)
			}
			if got.AdjudicatedBy != c.wantBy {
				t.Errorf("adjudicated by = %s, want %s", got.AdjudicatedBy, c.wantBy)
			}
			if got.Explanation == "" {
				t.Error("no explanation; an officer is told a route and not why")
			}
		})
	}
}

// The order the two happen to be recorded in must not change the outcome.
func TestConsensusIsSymmetric(t *testing.T) {
	forward := Reconcile([]Finding{talathi(30), krushi(41)})
	reverse := Reconcile([]Finding{krushi(41), talathi(30)})

	if forward.Route != reverse.Route {
		t.Errorf("route depends on recording order: %s vs %s",
			forward.Route, reverse.Route)
	}
	if *forward.Divergence != *reverse.Divergence {
		t.Errorf("divergence depends on order: %.2f vs %.2f",
			*forward.Divergence, *reverse.Divergence)
	}
	if *forward.Mean != *reverse.Mean {
		t.Errorf("mean depends on order: %.2f vs %.2f", *forward.Mean, *reverse.Mean)
	}
}

func TestConsensusMean(t *testing.T) {
	got := Reconcile([]Finding{talathi(40), krushi(44)})
	if got.Mean == nil || *got.Mean != 42 {
		t.Fatalf("mean = %v, want 42", got.Mean)
	}
	if *got.Divergence != 4 {
		t.Errorf("divergence = %.2f, want 4", *got.Divergence)
	}
}

// One officer finding a loss and the other finding none is the sharpest
// disagreement the system can see. Averaging it would bury exactly the case a
// supervisor needs to look at.
func TestDisagreementAboutWhetherThereWasALossEscalates(t *testing.T) {
	cases := map[string][]Finding{
		"rejected against confirmed": {
			talathi(45),
			pairFinding(rbac.RoleKrushiSahayak, OutcomeRejected, nil),
		},
		"untraceable against confirmed": {
			pairFinding(rbac.RoleTalathi, OutcomeNotTraceable, nil),
			krushi(45),
		},
	}

	for name, in := range cases {
		got := Reconcile(in)
		if !got.Complete {
			t.Fatalf("%s: not complete", name)
		}
		if got.Route != RouteEscalation {
			t.Errorf("%s: route = %s, want escalation", name, got.Route)
		}
		if got.AdjudicatedBy != rbac.RoleTehsildar {
			t.Errorf("%s: adjudicated by = %s, want tehsildar", name, got.AdjudicatedBy)
		}
		if got.Divergence != nil {
			t.Errorf("%s: produced a divergence between a figure and no figure", name)
		}
		if got.Mean != nil {
			t.Errorf("%s: averaged a rejection into a percentage", name)
		}
	}
}

// Both saying there was no loss is agreement, but there is still nothing to
// average, and someone above the pair should close it.
func TestBothRejectingStillGoesUp(t *testing.T) {
	got := Reconcile([]Finding{
		pairFinding(rbac.RoleTalathi, OutcomeRejected, nil),
		pairFinding(rbac.RoleKrushiSahayak, OutcomeRejected, nil),
	})
	if !got.Complete {
		t.Fatal("not complete")
	}
	if got.Route != RouteEscalation {
		t.Errorf("route = %s, want escalation", got.Route)
	}
	if got.Mean != nil {
		t.Error("produced a mean from two rejections")
	}
}

// Findings recorded at a higher tier must not be mistaken for the pair's.
func TestOnlyVillagePairFindingsCount(t *testing.T) {
	arb := Finding{
		OfficerID: "circle", Role: rbac.RoleMandalOfficer, Tier: TierArbitration,
		Outcome: OutcomeConfirmed, DamagePercent: f(50),
	}

	got := Reconcile([]Finding{talathi(40), arb})
	if got.Complete {
		t.Error("an arbitration finding was counted as the second of the pair")
	}

	got = Reconcile([]Finding{talathi(40), krushi(43), arb})
	if !got.Complete || got.Route != RouteConsensus {
		t.Errorf("the pair should still reconcile with a third finding present: %+v", got)
	}
	if *got.Divergence != 3 {
		t.Errorf("divergence = %.2f, want 3 — the arbitration figure leaked in",
			*got.Divergence)
	}
}

// A zero-loss finding is a figure, not an absence of one.
func TestZeroPercentIsAFigure(t *testing.T) {
	got := Reconcile([]Finding{
		pairFinding(rbac.RoleTalathi, OutcomeConfirmed, f(0)),
		pairFinding(rbac.RoleKrushiSahayak, OutcomeConfirmed, f(2)),
	})
	if got.Route != RouteConsensus {
		t.Errorf("route = %s, want consensus", got.Route)
	}
	if got.Mean == nil || *got.Mean != 1 {
		t.Errorf("mean = %v, want 1", got.Mean)
	}
}

// The thresholds are the specification's, and changing one silently changes
// who decides thousands of claims.
func TestThresholdsAreWhatTheSpecificationSays(t *testing.T) {
	if ConsensusThreshold != 5.0 {
		t.Errorf("consensus threshold = %v, want 5", ConsensusThreshold)
	}
	if ArbitrationThreshold != 12.0 {
		t.Errorf("arbitration threshold = %v, want 12", ArbitrationThreshold)
	}
}

// The pair never adjudicate their own verification.
func TestPairIsNeverTheAdjudicator(t *testing.T) {
	for _, c := range []struct{ a, b float64 }{{40, 40}, {30, 40}, {10, 90}} {
		got := Reconcile([]Finding{talathi(c.a), krushi(c.b)})
		if got.AdjudicatedBy == rbac.RoleTalathi || got.AdjudicatedBy == rbac.RoleKrushiSahayak {
			t.Errorf("%.0f vs %.0f routes back to the village pair", c.a, c.b)
		}
	}
}
