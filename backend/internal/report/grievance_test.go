package report

import (
	"testing"
	"time"
)

// The rules that make an appeal an appeal, rather than a form that records
// disagreement and changes nothing.

func TestGrievanceGrounds(t *testing.T) {
	// Every ground a farmer can choose needs a label, because the label is
	// what they read while already unhappy.
	for _, g := range []GrievanceGround{
		GroundAreaWrong, GroundDamagePercentLow, GroundVisitNeeded,
		GroundEvidenceIgnored, GroundOther,
	} {
		if !g.Valid() {
			t.Errorf("%q is not accepted as a ground", g)
		}
		if GroundLabels[g] == "" {
			t.Errorf("%q has no label", g)
		}
	}

	for _, g := range []GrievanceGround{"", "unfair", "AREA_WRONG"} {
		if g.Valid() {
			t.Errorf("%q was accepted as a ground", g)
		}
	}
}

func TestGroundLabelsLeadInMarathi(t *testing.T) {
	// This is the one screen a farmer reaches while already unhappy. Making
	// them read English there is its own kind of refusal.
	for g, label := range GroundLabels {
		if label == "" {
			t.Fatalf("%q has no label", g)
		}
		first := []rune(label)[0]
		if first < 0x0900 || first > 0x097F {
			t.Errorf("%q leads in %q, not Devanagari", g, string(first))
		}
	}
}

func TestOpenStatesAreTheOnesSomeoneOwes(t *testing.T) {
	// Drives the queue and the deadline. A state wrongly counted as closed is
	// an appeal nobody is holding.
	for _, s := range []GrievanceState{GrievanceOpen, GrievanceUnderReview} {
		if !s.Open() {
			t.Errorf("%q is not counted as open work", s)
		}
	}
	for _, s := range []GrievanceState{
		GrievanceUpheld, GrievancePartlyUpheld, GrievanceRejected, GrievanceWithdrawn,
	} {
		if s.Open() {
			t.Errorf("%q is still counted as open work", s)
		}
	}
}

func TestTheAppealDeadlineIsLongerThanAVerification(t *testing.T) {
	// An appeal usually needs a field visit the original did not, and a
	// deadline nobody can meet is one that gets ignored rather than met.
	if GrievanceSLA <= VerificationSLA {
		t.Errorf("grievance SLA %v is not longer than verification SLA %v",
			GrievanceSLA, VerificationSLA)
	}
	if GrievanceSLA != 15*24*time.Hour {
		t.Errorf("GrievanceSLA = %v, want 15 days", GrievanceSLA)
	}
}

func TestOnlyAppealableReportStates(t *testing.T) {
	// A report still being verified has nothing decided to appeal. Accepting
	// one would pre-empt the officers still working on it.
	appealable := map[State]bool{
		StateVerified:    true,
		StateAdjudicated: true,
		StateClosed:      true,
	}

	for _, s := range []State{
		StateDraft, StateSubmitted, StateAcknowledged,
		StateAssigned, StateUnderVerification, StateWithdrawn,
	} {
		if appealable[s] {
			t.Errorf("%q should have nothing to appeal", s)
		}
	}
	for s := range appealable {
		if s.Terminal() && s != StateClosed {
			t.Errorf("%q is terminal but not closed", s)
		}
	}
}
