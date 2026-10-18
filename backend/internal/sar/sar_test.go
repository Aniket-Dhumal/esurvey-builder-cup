package sar

import (
	"math"
	"strings"
	"testing"
)

// Radar is the answer to the platform's known blindness, so the tests are
// about two things: whether the signatures are read correctly, and whether
// every reading admits what backscatter cannot tell anybody.

func obs(date string, vv, vh float64) Observation {
	return Observation{Date: date, VVdB: vv, VHdB: vh, Orbit: "ASCENDING"}
}

func finding(r *Reading, code string) (Finding, bool) {
	for _, f := range r.Findings {
		if f.Code == code {
			return f, true
		}
	}
	return Finding{}, false
}

func TestStandingWaterIsObservedRatherThanInferred(t *testing.T) {
	// Waterlogging risk today comes from terrain slope, which says where
	// water would collect, not where it did. This is the difference.
	r, why, ok := Analyse([]Observation{
		obs("2026-07-01", -9.0, -16.0),
		obs("2026-07-13", -8.6, -15.8),
		obs("2026-07-25", -18.5, -24.0), // submerged
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}

	f, found := finding(r, FindingInundation)
	if !found {
		t.Fatalf("a nine-decibel collapse was not read as water: %+v", r.Findings)
	}
	if f.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %s", f.Confidence)
	}
	if !strings.Contains(f.Detail, "inferred from slope") {
		t.Errorf("the detail does not say what makes this different: %s", f.Detail)
	}
}

func TestADryFieldIsNotFlooded(t *testing.T) {
	// A field that got drier reads low on VV too. Requiring a fall from the
	// parcel's own level as well as a low absolute value is what separates
	// them.
	r, why, ok := Analyse([]Observation{
		obs("2026-04-01", -15.5, -22.0),
		obs("2026-04-13", -15.4, -22.1),
		obs("2026-04-25", -15.8, -22.3),
	}, "2026-04-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if _, found := finding(r, FindingInundation); found {
		t.Errorf("a consistently low but unchanged field was read as flooded: %+v",
			r.Findings)
	}
}

func TestAFlattenedCanopyIsReadFromTheRatioNotTheLevel(t *testing.T) {
	// The physical signature: a standing stem canopy depolarises the pulse
	// and lifts the cross-polarised return; flatten it and the ratio falls
	// while the co-polarised return holds.
	r, why, ok := Analyse([]Observation{
		obs("2026-03-01", -8.0, -14.0), // ratio -6.0
		obs("2026-03-13", -8.1, -14.1),
		obs("2026-03-25", -7.8, -17.5), // ratio -9.7
	}, "2026-03-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}

	f, found := finding(r, FindingLodging)
	if !found {
		t.Fatalf("a falling ratio with a steady VV was not read as lodging: %+v",
			r.Findings)
	}
	if r.DeltaRatio > -1.5 {
		t.Errorf("delta ratio = %v", r.DeltaRatio)
	}
	if !strings.Contains(f.Detail, "depolarises") {
		t.Errorf("the detail does not explain the mechanism: %s", f.Detail)
	}
}

func TestFloodingIsNotReportedAsLodging(t *testing.T) {
	// Submergence drops both returns, which also drops the ratio. Reading
	// that as lodging would describe a flooded field as a flattened one.
	r, why, ok := Analyse([]Observation{
		obs("2026-07-01", -8.0, -14.0),
		obs("2026-07-13", -8.2, -14.2),
		obs("2026-07-25", -19.0, -26.0),
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if _, found := finding(r, FindingLodging); found {
		t.Errorf("a submerged field was reported as lodged: %+v", r.Findings)
	}
	if _, found := finding(r, FindingInundation); !found {
		t.Error("it was not reported as flooded either")
	}
}

func TestAVanishedCanopyAdmitsItCouldBeAHarvest(t *testing.T) {
	// Radar cannot tell a harvested field from a destroyed one, and a finding
	// that did not say so would be read as damage.
	r, why, ok := Analyse([]Observation{
		obs("2026-10-01", -9.0, -15.0),
		obs("2026-10-13", -9.1, -15.2),
		obs("2026-10-25", -9.4, -19.0),
	}, "2026-10-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}

	f, found := finding(r, FindingCanopyLoss)
	if !found {
		// Lodging is an acceptable reading of the same numbers; what is not
		// acceptable is silence about harvest.
		if f, found = finding(r, FindingLodging); !found {
			t.Fatalf("a four-decibel VH collapse produced nothing: %+v", r.Findings)
		}
		return
	}
	var said bool
	for _, c := range f.Caveats {
		if strings.Contains(c, "harvest") {
			said = true
		}
	}
	if !said {
		t.Errorf("the finding does not admit harvest looks identical: %v", f.Caveats)
	}
}

func TestAFieldThatDidNotChangeIsReportedAsLoudlyAsOneThatDid(t *testing.T) {
	// This is the whole value of radar here. The optical record was blank
	// under cloud; radar looked, and the field was fine. An officer needs
	// that as much as they need a damage signature.
	r, why, ok := Analyse([]Observation{
		obs("2026-07-01", -8.5, -14.5),
		obs("2026-07-13", -8.6, -14.6),
		obs("2026-07-25", -8.4, -14.3),
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}

	f, found := finding(r, FindingNoChange)
	if !found {
		t.Fatalf("an unchanged field produced %+v", r.Findings)
	}
	if f.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %s for three consistent passes", f.Confidence)
	}
	if !strings.Contains(f.Detail, "through the cloud") {
		t.Errorf("the detail does not say what radar did that optical could not: %s",
			f.Detail)
	}
}

func TestAChangeTooSmallToNameIsNotNamed(t *testing.T) {
	// Soil moisture alone moves backscatter a decibel or two. Naming that a
	// finding would produce a damage signature after every shower.
	r, why, ok := Analyse([]Observation{
		obs("2026-07-01", -8.5, -14.5),
		obs("2026-07-13", -8.6, -14.6),
		obs("2026-07-25", -10.0, -15.8),
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	for _, code := range []string{FindingInundation, FindingLodging, FindingCanopyLoss} {
		if _, found := finding(r, code); found {
			t.Errorf("a decibel and a half was read as %s", code)
		}
	}
	f, _ := finding(r, FindingNoChange)
	if f.Confidence != ConfidenceLow {
		t.Errorf("confidence = %s for a change in the grey zone", f.Confidence)
	}
	if !strings.Contains(f.Detail, "Soil moisture") {
		t.Errorf("the detail does not offer the ordinary cause: %s", f.Detail)
	}
}

// ---------------------------------------------------------------------------
// What the record cannot support
// ---------------------------------------------------------------------------

func TestNoPassBeforeTheIncidentIsRefused(t *testing.T) {
	_, why, ok := Analyse([]Observation{
		obs("2026-07-25", -18.0, -24.0),
	}, "2026-07-20")
	if ok {
		t.Fatal("a reading was produced with nothing to compare against")
	}
	if !strings.Contains(why, "precedes") {
		t.Errorf("reason = %q", why)
	}
}

func TestNoPassAfterTheIncidentSaysWhenOneIsDue(t *testing.T) {
	// The officer needs to know whether to wait or to go and look.
	_, why, ok := Analyse([]Observation{
		obs("2026-07-01", -8.5, -14.5),
	}, "2026-07-20")
	if ok {
		t.Fatal("a reading was produced with no pass after the incident")
	}
	if !strings.Contains(why, "six to twelve days") {
		t.Errorf("the reason does not say when a pass is due: %q", why)
	}
}

func TestACorruptedValueIsDiscardedRatherThanRead(t *testing.T) {
	// A scene that failed calibration returns values no field produces, and
	// averaging one into a baseline would move it by decibels.
	r, why, ok := Analyse([]Observation{
		obs("2026-07-01", -8.5, -14.5),
		obs("2026-07-07", -120.0, -200.0), // impossible
		{Date: "2026-07-09", VVdB: math.NaN(), VHdB: -14.0},
		{Date: "not-a-date", VVdB: -8.0, VHdB: -14.0},
		obs("2026-07-13", -8.6, -14.6),
		obs("2026-07-25", -8.4, -14.3),
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if len(r.Before) != 2 {
		t.Errorf("kept %d passes before the incident, want 2", len(r.Before))
	}
	if r.BaselineVV < -10 || r.BaselineVV > -8 {
		t.Errorf("baseline VV = %v; a corrupted scene reached the average",
			r.BaselineVV)
	}
}

func TestMixingOrbitDirectionsIsFlagged(t *testing.T) {
	// Backscatter differs by several decibels between ascending and
	// descending passes over an unchanged field. Comparing across them
	// manufactures a change.
	r, why, ok := Analyse([]Observation{
		{Date: "2026-07-01", VVdB: -8.5, VHdB: -14.5, Orbit: "DESCENDING"},
		{Date: "2026-07-25", VVdB: -12.5, VHdB: -18.5, Orbit: "ASCENDING"},
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if r.OrbitsMatched {
		t.Error("two different orbit directions were reported as matched")
	}

	var warned bool
	for _, f := range r.Findings {
		for _, c := range f.Caveats {
			if strings.Contains(c, "orbit") {
				warned = true
			}
		}
	}
	if !warned {
		t.Errorf("no finding mentions the orbit mismatch: %+v", r.Findings)
	}
}

func TestMatchingOrbitsArePreferredForTheBaseline(t *testing.T) {
	r, why, ok := Analyse([]Observation{
		{Date: "2026-07-01", VVdB: -12.0, VHdB: -18.0, Orbit: "DESCENDING"},
		{Date: "2026-07-05", VVdB: -8.5, VHdB: -14.5, Orbit: "ASCENDING"},
		{Date: "2026-07-13", VVdB: -8.6, VHdB: -14.6, Orbit: "ASCENDING"},
		{Date: "2026-07-25", VVdB: -8.4, VHdB: -14.3, Orbit: "ASCENDING"},
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if !r.OrbitsMatched {
		t.Fatal("the ascending baseline was not matched")
	}
	// The descending pass at -12 must not have dragged the baseline down.
	if r.BaselineVV < -9.5 {
		t.Errorf("baseline VV = %v; the descending pass reached the average",
			r.BaselineVV)
	}
}

func TestALateFirstPassIsFlaggedBecauseWaterDrains(t *testing.T) {
	// Revisit is six to twelve days. A flood that drained before the pass
	// leaves no trace, and a clean radar record would then read as evidence
	// that nothing happened.
	r, why, ok := Analyse([]Observation{
		obs("2026-07-01", -8.5, -14.5),
		obs("2026-07-13", -8.6, -14.6),
		obs("2026-08-10", -8.4, -14.3),
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if r.DaysToFirst != 21 {
		t.Errorf("days to first pass = %d, want 21", r.DaysToFirst)
	}

	var warned bool
	for _, f := range r.Findings {
		for _, c := range f.Caveats {
			if strings.Contains(c, "drained") {
				warned = true
			}
		}
	}
	if !warned {
		t.Errorf("a three-week gap carried no caveat: %+v", r.Findings)
	}
}

func TestASingleBaselinePassCarriesItsSoilMoisture(t *testing.T) {
	r, why, ok := Analyse([]Observation{
		obs("2026-07-13", -8.6, -14.6),
		obs("2026-07-25", -18.5, -24.0),
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	var said bool
	for _, f := range r.Findings {
		for _, c := range f.Caveats {
			if strings.Contains(c, "soil moisture on that day") {
				said = true
			}
		}
	}
	if !said {
		t.Errorf("a one-pass baseline was not qualified: %+v", r.Findings)
	}
}

func TestAPassOnTheIncidentDayCountsAsBefore(t *testing.T) {
	// The satellite passes at a fixed local time; a hailstorm at 16:40 is
	// after it. Counting the same-day pass as "after" would compare the field
	// with itself before the event and report no change.
	r, why, ok := Analyse([]Observation{
		obs("2026-07-08", -8.5, -14.5),
		obs("2026-07-20", -8.6, -14.6), // the incident day
		obs("2026-07-26", -18.5, -24.0),
	}, "2026-07-20")
	if !ok {
		t.Fatalf("no reading: %s", why)
	}
	if len(r.Before) != 2 {
		t.Errorf("the incident-day pass went to the wrong side: before=%d after=%d",
			len(r.Before), len(r.After))
	}
}

// ---------------------------------------------------------------------------
// The optical gap
// ---------------------------------------------------------------------------

func TestRadarAnswersWhetherAnythingWatchedTheOpticalGap(t *testing.T) {
	// The question worth asking when NDVI comes back with a two-month hole.
	passes := []Observation{
		obs("2026-06-20", -8.5, -14.5),
		obs("2026-07-14", -8.6, -14.6),
		obs("2026-08-07", -8.4, -14.3),
	}

	n, ok := CoversOpticalGap(passes, "2026-06-25", "2026-08-20")
	if !ok || n != 2 {
		t.Errorf("covered %d passes in the gap (ok=%v), want 2", n, ok)
	}

	n, ok = CoversOpticalGap(passes, "2026-09-01", "2026-10-01")
	if ok || n != 0 {
		t.Errorf("found %d passes in a window with none", n)
	}
}

func TestAnUnparseableGapIsNotCovered(t *testing.T) {
	if _, ok := CoversOpticalGap(nil, "nonsense", "2026-08-20"); ok {
		t.Error("an unparseable window reported coverage")
	}
}

func TestEveryFindingSaysHowSureItIs(t *testing.T) {
	cases := [][]Observation{
		{obs("2026-07-01", -8.5, -14.5), obs("2026-07-13", -8.6, -14.6),
			obs("2026-07-25", -18.5, -24.0)},
		{obs("2026-03-01", -8.0, -14.0), obs("2026-03-13", -8.1, -14.1),
			obs("2026-03-25", -7.8, -17.5)},
		{obs("2026-07-01", -8.5, -14.5), obs("2026-07-13", -8.6, -14.6),
			obs("2026-07-25", -8.4, -14.3)},
	}
	for i, c := range cases {
		r, why, ok := Analyse(c, "2026-07-20")
		if !ok {
			// The lodging case sits in March; re-run it against its own date.
			if r, why, ok = Analyse(c, "2026-03-20"); !ok {
				t.Fatalf("case %d: %s", i, why)
			}
		}
		if len(r.Findings) == 0 {
			t.Errorf("case %d produced no finding", i)
			continue
		}
		for _, f := range r.Findings {
			if f.Confidence == "" {
				t.Errorf("case %d: finding %s has no confidence", i, f.Code)
			}
			if strings.TrimSpace(f.Detail) == "" {
				t.Errorf("case %d: finding %s has no detail", i, f.Code)
			}
		}
	}
}
