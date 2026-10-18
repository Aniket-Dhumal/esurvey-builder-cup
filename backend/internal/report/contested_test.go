package report

import "testing"

// When two officers standing in the same field disagree.
//
// The platform used to use this to decide *whether* a report went up. It now
// decides how it is treated on the way: every report climbs all four rungs,
// and a contested one sorts to the top of the queue, owes a written reason at
// the Circle, and cannot be swept into a bulk approval.
//
// So the flag is what stands between a genuine dispute and a tick, which makes
// it worth more than the three lines it takes to compute.

func pct(v float64) *float64 { return &v }

func TestAnIncompletePairIsNotContested(t *testing.T) {
	// Nothing has been disagreed with yet. Marking it now would put every
	// report at the top of the queue the moment the first officer filed.
	if contestedBy(Consensus{Complete: false}) {
		t.Error("a half-recorded pair was marked contested")
	}
}

func TestAgreementIsNotContested(t *testing.T) {
	c := Consensus{
		Complete:      true,
		FirstPercent:  pct(40),
		SecondPercent: pct(46),
		Divergence:    pct(6),
	}
	if contestedBy(c) {
		t.Error("six points apart was treated as a dispute")
	}
}

func TestTheThresholdIsTwentyFivePoints(t *testing.T) {
	// The same figure the assessment's cross-check uses between the
	// instruments and the panel, so one number means one thing platform-wide.
	if ContestedDivergence != 25.0 {
		t.Fatalf("ContestedDivergence = %v, want 25", ContestedDivergence)
	}

	at := Consensus{Complete: true, FirstPercent: pct(20), SecondPercent: pct(45),
		Divergence: pct(25)}
	if contestedBy(at) {
		t.Error("exactly at the threshold was treated as over it")
	}

	over := Consensus{Complete: true, FirstPercent: pct(20), SecondPercent: pct(46),
		Divergence: pct(26)}
	if !contestedBy(over) {
		t.Error("past the threshold was not marked contested")
	}
}

func TestARefusalAgainstAFigureIsAlwaysContested(t *testing.T) {
	// One officer saying "there is no loss here" and the other saying "forty
	// per cent" is the sharpest disagreement the system can see. Reconcile
	// leaves Divergence nil rather than inventing a number to subtract, so a
	// threshold test alone would let this through as agreement.
	cases := []struct {
		name          string
		first, second *float64
	}{
		{"the first refused", nil, pct(40)},
		{"the second refused", pct(40), nil},
		{"both refused", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := contestedBy(Consensus{
				Complete: true, FirstPercent: c.first, SecondPercent: c.second,
			})
			if !got {
				t.Error("a missing figure was not treated as a disagreement")
			}
		})
	}
}

func TestAMissingDivergenceWithBothFiguresIsNotContested(t *testing.T) {
	// Defensive: if both officers gave a figure, Reconcile computes the gap.
	// Should it ever not, the safe reading is that two recorded numbers with
	// no computed distance is not evidence of a dispute.
	c := Consensus{Complete: true, FirstPercent: pct(40), SecondPercent: pct(41)}
	if contestedBy(c) {
		t.Error("a nil divergence between two real figures was read as a dispute")
	}
}
