package api

import "testing"

// The date comparison that now decides whose record a farmer can reach.
//
// A Farmer ID is printed on documents and known around a village, so when a
// lookup is answered with a date of birth, that date is carrying the whole
// weight. These are about the two ways it could fail open: reading a date too
// liberally, and treating an unreadable one as a match.

func TestADateMatchesAcrossTheFormatsPeopleActuallyWrite(t *testing.T) {
	// The registry writes 01-01-1973. A handset date picker produces
	// 1973-01-01. A farmer reading off a document may type either, with
	// slashes. A string comparison would refuse all but one of these.
	registry := "01-01-1973"
	for _, given := range []string{
		"01-01-1973",
		"1973-01-01",
		"01/01/1973",
		"1973/01/01",
		"1-1-1973",
	} {
		if !sameDOB(registry, given) {
			t.Errorf("sameDOB(%q, %q) = false; it is the same day", registry, given)
		}
	}
}

func TestADifferentDayNeverMatches(t *testing.T) {
	for _, given := range []string{
		"02-01-1973", // a day out
		"01-02-1973", // the month and day swapped
		"01-01-1974", // a year out
	} {
		if sameDOB("01-01-1973", given) {
			t.Errorf("sameDOB(%q) matched a different date; this is the "+
				"whole control", given)
		}
	}
}

func TestAnUnreadableDateNeverMatches(t *testing.T) {
	// Normalisation failing must fall to "no", not to "compare the raw
	// strings". A record whose date is junk should be unreachable, not
	// reachable by anyone who types the same junk.
	for _, junk := range []string{"", "not a date", "1973", "00-00-0000", "32-01-1973"} {
		if sameDOB(junk, junk) {
			t.Errorf("two identical unreadable dates (%q) matched", junk)
		}
	}
	if sameDOB("01-01-1973", "") {
		t.Error("an empty answer matched a real date")
	}
}

func TestADateIsNotReadAmericanStyle(t *testing.T) {
	// 02-03-1973 is 2 March here and 3 February in the layout Go's reference
	// date makes easiest to reach for. Getting this backwards would let a
	// farmer in by naming the wrong day, and keep the right one out.
	day, ok := parseDay("02-03-1973")
	if !ok {
		t.Fatal("02-03-1973 could not be read")
	}
	if day != "1973-03-02" {
		t.Errorf("parseDay(02-03-1973) = %s, want 1973-03-02 -- the registry "+
			"writes day first", day)
	}
}
