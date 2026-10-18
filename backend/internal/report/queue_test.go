package report

import "testing"

// routeFor decides what the adjudication queue tells an officer is waiting on
// them. Getting it wrong in either direction has a cost: a report routed to
// consensus when it needed arbitration is a farmer waiting on nobody, and one
// routed to arbitration when the pair agreed is an adjudicator's time spent
// confirming a number two officers already agreed on.

func TestRouteForOnePairOfFindings(t *testing.T) {
	cases := []struct {
		name  string
		entry AdjudicationEntry
		want  Route
	}{
		{
			// Waiting on the second verifier. Not an adjudicator's problem
			// yet, and telling them otherwise fills the queue with work
			// nobody can do.
			name:  "only one finding filed",
			entry: AdjudicationEntry{FindingsFiled: 1, FirstPercent: f(40)},
			want:  RouteConsensus,
		},
		{
			name: "the pair agree",
			entry: AdjudicationEntry{
				FindingsFiled: 2, FirstPercent: f(38), SecondPercent: f(42),
				Divergence: f(4),
			},
			want: RouteConsensus,
		},
		{
			// Exactly at the consensus threshold still counts as agreement:
			// Reconcile uses "at or under", and a queue that disagreed with
			// it would send an officer to a report that then tells them there
			// is nothing to decide.
			name: "exactly at the consensus threshold",
			entry: AdjudicationEntry{
				FindingsFiled: 2, FirstPercent: f(35), SecondPercent: f(40),
				Divergence: f(ConsensusThreshold),
			},
			want: RouteConsensus,
		},
		{
			name: "a gap the Circle Officer must settle",
			entry: AdjudicationEntry{
				FindingsFiled: 2, FirstPercent: f(30), SecondPercent: f(40),
				Divergence: f(10),
			},
			want: RouteArbitration,
		},
		{
			name: "a gap wide enough to escalate",
			entry: AdjudicationEntry{
				FindingsFiled: 2, FirstPercent: f(20), SecondPercent: f(60),
				Divergence: f(40),
			},
			want: RouteEscalation,
		},
		{
			name: "exactly at the arbitration threshold escalates",
			entry: AdjudicationEntry{
				FindingsFiled: 2, FirstPercent: f(20), SecondPercent: f(32),
				Divergence: f(ArbitrationThreshold),
			},
			want: RouteEscalation,
		},
		{
			// A rejection is not a number to average. Two officers can both
			// say 0% and "agree" while one of them means the loss did not
			// happen -- those are different claims about the same field.
			name: "a rejection always reaches a human",
			entry: AdjudicationEntry{
				FindingsFiled: 2, Rejected: 1,
				FirstPercent: f(0), SecondPercent: f(0), Divergence: f(0),
			},
			want: RouteArbitration,
		},
		{
			name: "a rejection outweighs a narrow gap",
			entry: AdjudicationEntry{
				FindingsFiled: 2, Rejected: 1,
				FirstPercent: f(38), SecondPercent: f(40), Divergence: f(2),
			},
			want: RouteArbitration,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := routeFor(c.entry); got != c.want {
				t.Errorf("route = %q, want %q", got, c.want)
			}
		})
	}
}

func TestRouteForAgreesWithReconcile(t *testing.T) {
	// The queue's routing and the decision an adjudicator is shown when they
	// open the report must not drift apart. Same thresholds, same answer.
	if ConsensusThreshold >= ArbitrationThreshold {
		t.Fatal("the thresholds have been reordered; routeFor's cases are now unreachable")
	}

	justOver := ConsensusThreshold + 0.01
	e := AdjudicationEntry{
		FindingsFiled: 2, FirstPercent: f(30),
		SecondPercent: f(30 + justOver), Divergence: f(justOver),
	}
	if got := routeFor(e); got != RouteArbitration {
		t.Errorf("a divergence just over the consensus threshold routed to %q", got)
	}
}
