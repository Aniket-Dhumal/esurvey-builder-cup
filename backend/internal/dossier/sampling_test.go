package dossier

import (
	"strings"
	"testing"
)

// The section that says who chose the evidence.
//
// It exists because every photograph in this report was taken by the person
// being assessed. Its job is to let a reader tell a directed sample from a
// selected one -- so the tests are about the sentences that carry that
// distinction, not about the table rendering.

func fourStations(placement string) []StationCoverage {
	roles := []string{"low", "mid_left", "mid_right", "high"}
	out := make([]StationCoverage, 0, 4)
	for i, role := range roles {
		out = append(out, StationCoverage{
			StationNo:  i + 1,
			Role:       role,
			Placement:  placement,
			Lat:        18.1720 - float64(i)*0.0014,
			Lon:        75.8131,
			Captured:   true,
			CapturedAt: "2026-08-30",
			CaptureID:  "cap-" + role,
		})
	}
	return out
}

func TestTheSectionIsNeverHalfWritten(t *testing.T) {
	near, far := 6.0, 40.0
	moved := fourStations("terrain")
	moved[3].OffsetM = &far
	moved[0].OffsetM = &near

	missing := fourStations("geometric")
	missing[2].Captured = false

	for _, s := range []Section{
		SamplingCoverage(nil, nil, 15),
		SamplingCoverage(fourStations("terrain"), nil, 15),
		SamplingCoverage(fourStations("geometric"), nil, 15),
		SamplingCoverage(moved, nil, 15),
		SamplingCoverage(missing, nil, 15),
	} {
		checkSection(t, s)
	}
}

func TestNoSamplingSaysTheFarmerChoseTheEvidence(t *testing.T) {
	// The most consequential sentence in the section. A claim with no assigned
	// points has evidence chosen by the claimant, and a reader who is not told
	// that will weigh it as though it had been sampled.
	s := SamplingCoverage(nil, nil, 15)

	if !strings.Contains(s.Preamble.EN, "wherever the farmer chose") {
		t.Errorf("an unassigned claim must say the farmer chose the evidence: %q",
			s.Preamble.EN)
	}
	if !strings.Contains(s.Preamble.EN, "not necessarily representative") {
		t.Errorf("it must say what that costs: %q", s.Preamble.EN)
	}
	if s.Preamble.MR == "" {
		t.Error("the farmer's half of the sentence is missing")
	}
}

func TestDirectedSamplingSaysTheFarmerDidNotChoose(t *testing.T) {
	// The claim the whole mechanism exists to be able to make, and the one
	// that answers a selective-evidence challenge at appeal.
	s := SamplingCoverage(fourStations("terrain"), nil, 15)

	if !strings.Contains(s.Preamble.EN, "not by the farmer") {
		t.Errorf("the preamble must say who assigned the points: %q", s.Preamble.EN)
	}
	if !strings.Contains(s.Preamble.EN, "before the walk began") {
		t.Errorf("it must say the points were fixed in advance, which is what "+
			"makes 'directed' true: %q", s.Preamble.EN)
	}
}

func TestNothingHereDeterminesTheFigure(t *testing.T) {
	// Coverage does not measure damage. It measures whether the things that do
	// were looking at a representative part of the field. Marked as
	// determining, it would read as evidence of loss.
	for _, s := range []Section{
		SamplingCoverage(fourStations("terrain"), nil, 15),
		SamplingCoverage(fourStations("geometric"), nil, 15),
		SamplingCoverage(nil, nil, 15),
	} {
		for _, r := range s.Rows {
			if r.Role == Determines {
				t.Errorf("§%s %q is marked Determines", s.Number, r.Parameter.EN)
			}
		}
	}
}

func TestAFlatParcelMakesNoClaimAboutHighAndLow(t *testing.T) {
	// Under geometric placement neither end is higher in any way a reader
	// could check. Calling one "the low end" would be an unfounded statement
	// about the land, printed on a compensation record.
	s := SamplingCoverage(fourStations("geometric"), nil, 15)

	for _, r := range s.Rows {
		if strings.Contains(strings.ToLower(r.Parameter.EN), "low end of the parcel") ||
			strings.Contains(strings.ToLower(r.Parameter.EN), "high end") {
			t.Errorf("a flat parcel names a station %q", r.Parameter.EN)
		}
	}

	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n.EN, "No claim is made that either end is lower") {
			said = true
		}
	}
	if !said {
		t.Error("a flat parcel must say explicitly that neither end is lower")
	}
}

func TestASlopingParcelExplainsWhyTheOrderMatters(t *testing.T) {
	s := SamplingCoverage(fourStations("terrain"), nil, 15)

	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n.EN, "wet part") && strings.Contains(n.EN, "dry part") {
			said = true
		}
	}
	if !said {
		t.Error("the slope note must say what following it achieves: both the " +
			"wet and the dry part end up in the evidence")
	}
}

func TestRecordingAwayFromAPointIsNotReportedAsAFailure(t *testing.T) {
	// The farmer is never required to stand on a suggested point: they record
	// from where they are inside the field, and the walk asks only that each
	// recording be a set distance from the others.
	//
	// This row used to read "the farmer could not reach the assigned point and
	// stopped 40 m short" -- a failure nobody asked them to avoid, printed in
	// the document that decides their payment. It has to say what the distance
	// costs in coverage without charging it to the farmer as a shortfall.
	offset := 40.0
	stations := fourStations("terrain")
	stations[3].OffsetM = &offset

	s := SamplingCoverage(stations, nil, 15)
	var row *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "High end") {
			row = &s.Rows[i]
		}
	}
	if row == nil {
		t.Fatal("the moved station is missing from the table")
	}

	if !strings.Contains(row.Effect.EN, "40 m") {
		t.Errorf("the distance must be in the sentence: %q", row.Effect.EN)
	}
	// What it costs: one part of the parcel is less well sampled.
	if !strings.Contains(row.Effect.EN, "under-represented") {
		t.Errorf("it must say what the distance cost in coverage: %q", row.Effect.EN)
	}
	// And what it does not mean.
	if !strings.Contains(row.Effect.EN, "permitted") {
		t.Errorf("it must say the farmer was allowed to record there: %q",
			row.Effect.EN)
	}
	for _, bad := range []string{"could not reach", "short", "failed"} {
		if strings.Contains(row.Effect.EN, bad) {
			t.Errorf("the row reports a failure the farmer never committed "+
				"(%q): %q", bad, row.Effect.EN)
		}
	}
}

func TestThePreambleSaysWhatTheWalkActuallyRequired(t *testing.T) {
	// Without this, a column headed "m from the point" reads as a column of
	// shortfalls, whatever the rows say further along the line.
	s := SamplingCoverage(fourStations("terrain"), nil, 15)

	if !strings.Contains(s.Preamble.EN, "not required to stand on them") {
		t.Errorf("the preamble does not say the points are not compulsory: %q",
			s.Preamble.EN)
	}
	if !strings.Contains(s.Preamble.EN, "at least 15 m") {
		t.Errorf("the preamble does not state the spacing that was enforced: %q",
			s.Preamble.EN)
	}
	if !strings.Contains(s.Preamble.EN, "not a shortfall") {
		t.Errorf("the preamble does not say what the distances are not: %q",
			s.Preamble.EN)
	}
	if s.Preamble.MR == s.Preamble.EN {
		t.Error("the preamble is not translated")
	}
}

func TestAnUnknownSpacingStatesTheRuleWithoutInventingAFigure(t *testing.T) {
	// A parcel whose area is not known still has the rule applied to it; what
	// is missing is the number, and a report that made one up would be
	// stating a requirement nobody was held to.
	s := SamplingCoverage(fourStations("terrain"), nil, 0)

	if !strings.Contains(s.Preamble.EN, "not required to stand on them") {
		t.Errorf("the rule was dropped along with the figure: %q", s.Preamble.EN)
	}
	if strings.Contains(s.Preamble.EN, "0 m") {
		t.Errorf("an unknown spacing printed as a figure: %q", s.Preamble.EN)
	}
}

func TestOrdinaryScatterIsNotReportedAsAMovedStation(t *testing.T) {
	// Six metres is a good fix, not a farmer who could not reach the point.
	// Reporting it as a move would put a caveat on every station in the
	// report and make the real ones invisible.
	offset := 6.0
	stations := fourStations("terrain")
	stations[0].OffsetM = &offset

	s := SamplingCoverage(stations, nil, 15)
	if !strings.Contains(s.Rows[0].Effect.EN, "ordinary GNSS scatter") {
		t.Errorf("a good fix should be described as scatter: %q", s.Rows[0].Effect.EN)
	}
	if len(s.Rows[0].Caveats) != 0 {
		t.Error("ordinary scatter must not be caveated")
	}
}

func TestFullCoverageIsClaimedOnlyWhenItIsTrue(t *testing.T) {
	full := SamplingCoverage(fourStations("terrain"), nil, 15)

	var claimed bool
	for _, n := range full.Notes {
		if strings.Contains(n.EN, "each of the 4 points") {
			claimed = true
		}
	}
	if !claimed {
		t.Error("four captured stations should say so")
	}

	// And with one missing, the claim must not be made.
	partial := fourStations("terrain")
	partial[2].Captured = false
	s := SamplingCoverage(partial, nil, 15)

	for _, n := range s.Notes {
		if strings.Contains(n.EN, "each of the 4 points") {
			t.Errorf("full coverage claimed with a station missing: %q", n.EN)
		}
	}

	var honest bool
	for _, n := range s.Notes {
		if strings.Contains(n.EN, "3 of 4") &&
			strings.Contains(n.EN, "have not been examined") {
			honest = true
		}
	}
	if !honest {
		t.Error("partial coverage must say which part of the parcel went unseen")
	}
}

func TestAStationWithNothingFromItIsAnAbsence(t *testing.T) {
	// Not a blank row. An absence with a stated reason is the difference
	// between "nothing came back from here" and a reader assuming the row was
	// a formatting slip.
	stations := fourStations("terrain")
	stations[1].Captured = false

	s := SamplingCoverage(stations, nil, 15)

	var found bool
	for _, r := range s.Rows {
		if r.Absent {
			found = true
			if r.AbsentWhy.MR == "" || r.AbsentWhy.EN == "" {
				t.Error("the absence is not explained in both languages")
			}
		}
	}
	if !found {
		t.Error("a station with no evidence must print as an absent row")
	}
}

func TestTheSectionComesBeforeTheEvidenceItGoverns(t *testing.T) {
	// Order is the point. A reader who sees the photographs first has already
	// formed a view of them by the time they learn who chose where to stand.
	sampling := SamplingCoverage(fourStations("terrain"), nil, 15)
	if sampling.Number != "13" {
		t.Errorf("sampling is §%s; it must precede the evidence list",
			sampling.Number)
	}
	if Evidence(nil).Number != "14" {
		t.Errorf("the evidence list is §%s; sampling must come first",
			Evidence(nil).Number)
	}
}
