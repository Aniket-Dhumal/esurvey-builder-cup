package livespend

import (
	"testing"
)

// What a cap must get right.
//
// The arithmetic is the whole of it, and the direction it errs in is what matters: a
// farmer refused time they never used is worse than one who gets slightly more than
// their allowance, because the first is a farmer standing in a field being told no for
// a reason that is not true.

func store() *Store {
	return (&Store{}).WithCaps(Caps{
		PerFarmerSeconds:  30 * 60,
		PerAccountSeconds: 90 * 60,
	})
}

func TestAFreshDayHasTheWholeAllowance(t *testing.T) {
	if got := store().Remaining(Used{}); got != 30*60 {
		t.Fatalf("remaining %d, want %d", got, 30*60)
	}
}

func TestTheSmallerOfTheTwoCeilingsGoverns(t *testing.T) {
	// Both apply. A farmer well under their own ceiling, on a handset that has spent
	// the account's day acting for the rest of the household, has none left -- and
	// reporting the larger figure would promise time that is then refused.
	s := store()
	got := s.Remaining(Used{FarmerSeconds: 60, AccountSeconds: 90 * 60})
	if got != 0 {
		t.Fatalf("remaining %d, want 0: the account ceiling is reached", got)
	}
}

func TestTheFarmerCeilingGovernsWhenItIsTheSmaller(t *testing.T) {
	s := store()
	got := s.Remaining(Used{FarmerSeconds: 29 * 60, AccountSeconds: 0})
	if got != 60 {
		t.Fatalf("remaining %d, want 60", got)
	}
}

func TestGoingOverNeverReportsNegativeTime(t *testing.T) {
	// A negative remaining would be handed to the relay as a session length, and a
	// session of minus four minutes is a bug somewhere downstream rather than a
	// refusal here.
	s := store()
	if got := s.Remaining(Used{FarmerSeconds: 40 * 60}); got != 0 {
		t.Fatalf("remaining %d, want 0", got)
	}
}

func TestTheCapsAreReportableSoAHandsetCanShowThem(t *testing.T) {
	// A farmer who is refused should be able to be told what the limit was, rather
	// than only that they reached it.
	c := store().Caps()
	if c.PerFarmerSeconds != 30*60 || c.PerAccountSeconds != 90*60 {
		t.Fatalf("caps %+v", c)
	}
}

func TestTheDefaultsAreTheOnesTheCommentDescribes(t *testing.T) {
	// Half an hour a farmer, an hour and a half a handset. Pinned because they are a
	// judgement rather than a measurement, and a silent change to them is a silent
	// change to what the feature costs.
	d := Default()
	if d.PerFarmerSeconds != 1800 {
		t.Fatalf("per farmer %d seconds", d.PerFarmerSeconds)
	}
	if d.PerAccountSeconds != 5400 {
		t.Fatalf("per account %d seconds", d.PerAccountSeconds)
	}
	if d.PerAccountSeconds <= d.PerFarmerSeconds {
		t.Fatal("a shared handset must be allowed more than one farmer, since a " +
			"household shares a phone")
	}
}

func TestTheFallbackGrantIsSmallButUseful(t *testing.T) {
	// Handed out when the count could not be read. Unlimited would be a runaway on
	// the day the database is slow; zero would refuse a farmer standing in a field
	// for a reason that has nothing to do with them.
	if SmallestUsefulSeconds <= 0 {
		t.Fatal("a counting failure must not refuse the farmer outright")
	}
	if SmallestUsefulSeconds > 300 {
		t.Fatalf("%d seconds is not a fallback, it is an allowance",
			SmallestUsefulSeconds)
	}
}
