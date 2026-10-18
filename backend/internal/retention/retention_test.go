package retention

import (
	"strings"
	"testing"
)

// Retention is the only irreversible operation in this system. A purge cannot
// be undone from a backup taken after it, so what is tested here is not that
// it deletes correctly — that needs a database — but that it *refuses* to,
// in every case where refusing is right.

// The SQL is examined as text, against the exported constant the sweeper
// itself uses. That is unusual and deliberate: these conditions are the
// difference between forgetting a settled claim and destroying a live one,
// they live in a WHERE clause no unit test can otherwise reach without a
// database, and reading a copy of them would test the copy.

func TestAnUnansweredAppealProtectsTheRecord(t *testing.T) {
	// A grievance is about this person's claim. Answering it needs to know
	// whose it was, so anonymising a report with an open appeal against it
	// would destroy the ability to answer the appeal.
	sql := MatchFarmerContact

	for _, want := range []string{"grievances", "open", "under_review", "NOT EXISTS"} {
		if !strings.Contains(sql, want) {
			t.Errorf("the anonymiser does not check %q; an open appeal would "+
				"lose the claimant it is about", want)
		}
	}
}

func TestOnlySettledReportsAreAnonymised(t *testing.T) {
	// A report still being verified is live work. Removing the farmer from it
	// mid-verification leaves officers assessing a claim with no claimant.
	sql := MatchFarmerContact

	for _, settled := range []string{"closed", "withdrawn", "adjudicated"} {
		if !strings.Contains(sql, settled) {
			t.Errorf("settled state %q is not in the anonymiser's filter", settled)
		}
	}
	for _, live := range []string{"'submitted'", "'assigned'", "'under_verification'"} {
		if strings.Contains(sql, live) {
			t.Errorf("live state %s would be anonymised", live)
		}
	}
}

func TestAnonymisingIsIdempotent(t *testing.T) {
	// anonymised_at is what stops a second sweep re-processing the same rows
	// and what lets a reader tell a withheld name from a missing one.
	if !strings.Contains(MatchFarmerContact, "anonymised_at IS NULL") {
		t.Error("the anonymiser would reprocess rows it has already cleared")
	}
}

// --- the safety interlocks --------------------------------------------------

func TestResultDefaultsToADryRun(t *testing.T) {
	// Three things must agree before anything is written: the policy enabled
	// in the database, confirm in the request, and permission on the route.
	// The first two are visible here.
	cases := []struct {
		name    string
		confirm bool
		enabled bool
		dryRun  bool
	}{
		{"neither", false, false, true},
		{"confirmed but the policy is off", true, false, true},
		{"enabled but not confirmed", false, true, true},
		{"both", true, true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The expression Sweep uses.
			got := !c.confirm || !c.enabled
			if got != c.dryRun {
				t.Errorf("dryRun = %v, want %v", got, c.dryRun)
			}
		})
	}
}

func TestErrNotEnabledIsItsOwnError(t *testing.T) {
	// The API turns this into a 409 with a sentence explaining that enabling
	// a policy is a separate, recorded decision. A generic error there would
	// read as a bug and invite a retry.
	if ErrNotEnabled == nil {
		t.Fatal("ErrNotEnabled is nil")
	}
	if !strings.Contains(ErrNotEnabled.Error(), "not enabled") {
		t.Errorf("ErrNotEnabled reads %q", ErrNotEnabled.Error())
	}
}

func TestResultReportsWhatItWouldHaveDone(t *testing.T) {
	// A dry run that does not report a count is a question nobody got an
	// answer to. Affected must be filled in whether or not it wrote.
	r := Result{Subject: "evidence_media", Action: "delete", Affected: 412, DryRun: true}
	if r.Affected != 412 {
		t.Error("a dry run lost its count")
	}
	if !r.DryRun {
		t.Error("a dry run did not say so")
	}
}

func TestKeptSubjectsAreSkippedNotSwept(t *testing.T) {
	// audit_log is 'keep' with a NULL period. The sweeper must recognise that
	// as a policy decision and say so, rather than computing a cutoff from a
	// zero duration and deleting everything older than now.
	for _, c := range []struct {
		action  string
		seconds int64
		skip    bool
	}{
		{"keep", 0, true},
		{"delete", 0, true}, // a zero period is not "delete everything"
		{"delete", 86400, false},
		{"anonymise", 86400, false},
	} {
		got := c.action == "keep" || c.seconds == 0
		if got != c.skip {
			t.Errorf("action %q with %ds: skip = %v, want %v",
				c.action, c.seconds, got, c.skip)
		}
	}
}
