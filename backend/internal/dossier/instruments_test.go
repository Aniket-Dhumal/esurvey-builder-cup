package dossier

import (
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/sar"
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// The instrument sections are the ones most likely to be misread. None of
// them measures damage, and every one of them looks as though it does.

func TestInstrumentSectionsAreNeverHalfWritten(t *testing.T) {
	sections := []Section{
		Weather(nil, nil, Case{Calamity: "hailstorm"}),
		Weather(fullWindow(), nil, Case{Calamity: "hailstorm"}),
		Weather(fullWindow(), nil, Case{Calamity: "flood"}),
		Optical(nil, Case{}),
		Optical(fullSatellite(), Case{}),
		Optical(noVegetation(), Case{}),
		Radar(nil, nil, "radar unavailable for this parcel", Case{}),
		Radar(fullFinding(sar.FindingNoChange), fullReading(), "", Case{}),
		Radar(fullFinding(sar.FindingInundation), fullReading(), "", Case{}),
		TerrainSection(nil, Case{}),
		TerrainSection(&assessment.Terrain{
			MeanSlopeDegrees: 1.2,
			Classification:   "nearly level",
			WaterloggingRisk: "high",
		}, Case{Calamity: "flood"}),
	}
	for _, s := range sections {
		checkSection(t, s)
	}
}

func TestNothingFromOrbitDeterminesTheFigure(t *testing.T) {
	// A fall in NDVI is a fall in greenness, which a harvest produces as
	// readily as a hailstorm. A collapse in backscatter is a smooth surface,
	// which a flooded field and a rolled one share. Neither measures damage,
	// and the markers are what keep the table from saying they do.
	for _, s := range []Section{
		Optical(fullSatellite(), Case{}),
		Radar(fullFinding(sar.FindingLodging), fullReading(), "", Case{}),
		TerrainSection(&assessment.Terrain{MeanSlopeDegrees: 1.2}, Case{Calamity: "flood"}),
		Weather(fullWindow(), nil, Case{Calamity: "hailstorm"}),
	} {
		for _, r := range s.Rows {
			if r.Role == Determines {
				t.Errorf("§%s %q is marked Determines", s.Number, r.Parameter.EN)
			}
		}
	}
}

func TestRRVLSaysAHarvestLooksTheSame(t *testing.T) {
	// The single most available misreading in the whole document: a farmer
	// who harvested early produces the same curve as one who lost the crop.
	s := Optical(fullSatellite(), Case{})

	var rrvl *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "RRVL") {
			rrvl = &s.Rows[i]
		}
	}
	if rrvl == nil {
		t.Fatal("RRVL should be reported")
	}
	if !strings.Contains(rrvl.Effect.EN, "harvest") {
		t.Errorf("the effect must say a harvest produces the same fall: %q",
			rrvl.Effect.EN)
	}
}

func TestALongGapWarnsRatherThanReassures(t *testing.T) {
	// A reader not shown the gap over-trusts the delta. This row is what
	// stops the section reading as more certain than it is.
	long := fullSatellite()
	long.LargestGapDays = 24

	var gap *Row
	s := Optical(long, Case{})
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "Largest gap") {
			gap = &s.Rows[i]
		}
	}
	if gap == nil {
		t.Fatal("the gap should be reported")
	}
	if !strings.Contains(gap.Effect.EN, "long gap") {
		t.Errorf("a 24-day gap should be called long: %q", gap.Effect.EN)
	}
}

func TestNoMaterialChangeIsReportedAsLoudlyAsTheRest(t *testing.T) {
	// Radar saw the field through the cloud and it did not change. Burying
	// that would make the section an instrument that only ever agrees, which
	// is worth nothing to an appeal.
	s := Radar(fullFinding(sar.FindingNoChange), fullReading(), "", Case{})

	if len(s.Rows) == 0 {
		t.Fatal("the finding should be reported")
	}
	e := s.Rows[0].Effect.EN
	if !strings.Contains(e, "does not agree") {
		t.Errorf("it must say the finding disagrees with the claim: %q", e)
	}
	if s.Rows[0].Absent {
		t.Error("no-change is a finding, not an absence")
	}
}

func TestUncodedHailDoesNotRefuteAHailClaim(t *testing.T) {
	// Hail is intensely local and the observation grid is coarse. A report
	// that let "no hail coded" read as "no hail fell" would cost farmers
	// claims for a limitation of the instrument.
	w := fullWindow()
	w.HailDays = 0

	s := Weather(w, nil, Case{Calamity: "hailstorm"})
	var hail *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "Hail") {
			hail = &s.Rows[i]
		}
	}
	if hail == nil {
		t.Fatal("hail days should be reported")
	}
	if !strings.Contains(hail.Effect.EN, "does not refute") {
		t.Errorf("absence of hail coding must not read as refutation: %q",
			hail.Effect.EN)
	}
}

func TestACrossOrbitComparisonIsCaveated(t *testing.T) {
	// Backscatter differs by several decibels between ascending and
	// descending passes for the same unchanged field, so a cross-orbit
	// comparison shows changes that did not happen.
	r := fullReading()
	r.OrbitsMatched = false

	s := Radar(fullFinding(sar.FindingLodging), r, "", Case{})

	var found bool
	for _, row := range s.Rows {
		for _, c := range row.Caveats {
			if strings.Contains(c.EN, "different orbits") {
				found = true
			}
		}
	}
	if !found {
		t.Error("a cross-orbit comparison must be caveated")
	}
}

func TestALateRadarPassSaysWhatItCouldHaveMissed(t *testing.T) {
	// Water that drained before the pass leaves no trace. An absence of
	// inundation must not read as an absence of flooding.
	r := fullReading()
	r.DaysToFirst = 9

	s := Radar(fullFinding(sar.FindingNoChange), r, "", Case{})
	var timing *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "First radar pass") {
			timing = &s.Rows[i]
		}
	}
	if timing == nil {
		t.Fatal("the timing should be reported")
	}
	if !strings.Contains(timing.Effect.EN, "does not mean") {
		t.Errorf("it must say what the gap could have hidden: %q", timing.Effect.EN)
	}
}

func TestMissingWeatherDaysAreStated(t *testing.T) {
	w := fullWindow()
	w.Missing = 5

	s := Weather(w, nil, Case{Calamity: "flood"})
	var said bool
	for _, n := range s.Notes {
		if strings.Contains(n.EN, "no record") {
			said = true
		}
	}
	if !said {
		t.Error("a window with missing days must say so; the figures are drawn " +
			"from fewer days than the window claims")
	}
}

func TestNoCropDetectedIsFlaggedForChecking(t *testing.T) {
	// A serious finding, and one a heavily clouded season can produce
	// wrongly. It must reach an officer as something to verify rather than as
	// a conclusion -- against the photographs first, since nobody visits, and
	// with a route to a visit where doubt survives that.
	s := Optical(noVegetation(), Case{})

	var found bool
	for _, r := range s.Rows {
		if strings.Contains(r.Parameter.EN, "crop detected") {
			found = true
			if !strings.Contains(r.Effect.EN, "against the photographs") {
				t.Errorf("it must send officers to the evidence: %q", r.Effect.EN)
			}
			if !strings.Contains(r.Effect.EN, "ask for a visit") {
				t.Errorf("and offer the exception where doubt remains: %q",
					r.Effect.EN)
			}
		}
	}
	if !found {
		t.Error("a season with no vegetation detected must say so explicitly")
	}
}

func TestAnAbsentInstrumentSaysWhyInBothLanguages(t *testing.T) {
	for _, s := range []Section{
		Optical(nil, Case{}),
		Radar(nil, nil, "", Case{}),
		Weather(nil, nil, Case{Calamity: "flood"}),
		TerrainSection(nil, Case{}),
	} {
		if len(s.Rows) != 1 || !s.Rows[0].Absent {
			t.Fatalf("§%s should print one absent row", s.Number)
		}
		if s.Rows[0].AbsentWhy.MR == "" || s.Rows[0].AbsentWhy.EN == "" {
			t.Errorf("§%s absence is not explained in both languages", s.Number)
		}
	}
}

// --- fixtures ---------------------------------------------------------------

func fullWindow() *weather.Window {
	return &weather.Window{
		From: "2026-08-15", To: "2026-08-29", Days: 14,
		RainMM: 186, RainDays: 9, HeavyDays: 3,
		MaxDailyMM: 92, MaxDailyOn: "2026-08-26",
		MaxGustKmh: 74, MaxGustOn: "2026-08-26",
		HailDays: 1, ThunderDays: 3,
		HottestC: 33.1, ColdestC: 21.4,
		LongestDrySpell: 4, LongestDrySpellFrom: "2026-08-17",
	}
}

func fullSatellite() *assessment.Satellite {
	yes := true
	return &assessment.Satellite{
		BaselineNDVI: 0.71, BaselineDate: "2026-08-12",
		IncidentNDVI: 0.29, IncidentDate: "2026-08-28",
		DeltaNDVI: 0.42, RRVLPercent: 62,
		ObservationCount: 9, LargestGapDays: 6,
		VegetationDetected: &yes,
	}
}

func noVegetation() *assessment.Satellite {
	no := false
	s := fullSatellite()
	s.VegetationDetected = &no
	s.BaselineNDVI = 0.14
	return s
}

func fullFinding(code string) *sar.Finding {
	return &sar.Finding{
		Code:       code,
		Confidence: sar.ConfidenceModerate,
		Detail:     "VH fell 4.2 dB while VV held",
	}
}

func fullReading() *sar.Reading {
	return &sar.Reading{
		BaselineVV: -8.1, BaselineVH: -14.2, BaselineRatio: -6.1,
		IncidentVV: -8.4, IncidentVH: -18.4, IncidentRatio: -10.0,
		DeltaVV: -0.3, DeltaVH: -4.2, DeltaRatio: -3.9,
		FirstAfter: "2026-08-30", DaysToFirst: 4,
		OrbitsMatched: true,
	}
}

// ---------------------------------------------------------------------------
// The total an officer should not lean on
// ---------------------------------------------------------------------------

func TestASeasonBuiltFromTracesIsQualifiedInBothLanguages(t *testing.T) {
	// Barshi's kharif: 468 mm reads as an adequate season, and the district
	// had declared drought over it. Half the wet days held under 2.5 mm,
	// which is the reanalysis averaging convection over its cell rather than
	// rain on this field. Printing 468 mm unqualified hands the officer the
	// least reliable number in the section as the most convincing one.
	w := fullWindow()
	w.RainMM, w.RainDays, w.TraceDays = 468.8, 49, 52
	w.LongestDrySpell, w.LongestDrySpellFrom = 15, "2026-09-05"

	s := Weather(w, nil, Case{Calamity: "drought"})

	var note *Text
	for i, n := range s.Notes {
		if strings.Contains(n.EN, "upper bound") {
			note = &s.Notes[i]
		}
	}
	if note == nil {
		t.Fatalf("the total is printed without its caveat: %+v", s.Notes)
	}

	// Tf shares one argument list across both halves, so the two can disagree
	// silently and print "49 days held only a trace" in one language.
	for _, want := range []string{"49", "52"} {
		if !strings.Contains(note.EN, want) {
			t.Errorf("English note is missing %s: %s", want, note.EN)
		}
		if !strings.Contains(note.MR, want) {
			t.Errorf("Marathi note is missing %s: %s", want, note.MR)
		}
	}
	if strings.Index(note.EN, "49") > strings.Index(note.EN, "52") ||
		strings.Index(note.MR, "49") > strings.Index(note.MR, "52") {
		t.Errorf("rain days and trace days are in opposite orders:\nMR %s\nEN %s",
			note.MR, note.EN)
	}
}

func TestAnOrdinaryWindowCarriesNoSuchCaveat(t *testing.T) {
	// fullWindow is 186 mm over 9 rain days with no trace band worth naming.
	for _, n := range Weather(fullWindow(), nil, Case{Calamity: "hailstorm"}).Notes {
		if strings.Contains(n.EN, "upper bound") {
			t.Errorf("an ordinary window is second-guessed: %s", n.EN)
		}
	}
}

// ---------------------------------------------------------------------------
// The state's gauges, and the model that disagrees with them
// ---------------------------------------------------------------------------

func barshiGauges() *weather.Official {
	return &weather.Official{
		Source:    "maharain_mh_gov_in",
		UnitLevel: "sub_district",
		UnitName:  "Barshi",
		District:  "Solapur",
		CropYear:  2026,
		Months: []weather.OfficialPeriod{
			{Month: 8, Label: "August 2026", NormalMM: 124.5, ActualMM: 19.4, PercentOfNormal: 15.6},
			{Month: 9, Label: "September 2026", NormalMM: 161.1, ActualMM: 50.4, PercentOfNormal: 31.3},
		},
		Season: &weather.OfficialPeriod{
			Label:    "1 June to 28 September 2026",
			NormalMM: 516.6, ActualMM: 286.0, PercentOfNormal: 55.4,
		},
	}
}

func TestTheGaugeFigureComesFirstAndSettlesTheAmount(t *testing.T) {
	// 505 mm from a model above 286 mm from gauges, both labelled "total
	// rainfall", is a table that invites an officer to cite the wrong one -- and
	// the wrong one is the one that makes a declared drought disappear.
	s := Weather(fullWindow(), barshiGauges(), Case{Calamity: "drought"})

	if len(s.Rows) == 0 {
		t.Fatal("no rows")
	}
	first := s.Rows[0]
	if !strings.Contains(first.Parameter.EN, "against normal") {
		t.Errorf("the first row is %q, not the gauge figure", first.Parameter.EN)
	}
	// Corroborates, never Determines. It settles whether the rain failed, not
	// how badly this crop was hurt: Barshi's season came to 55% of normal with
	// a dense green canopy in the farmer's own footage. A rainfall deficit that
	// determined a damage figure would pay out on that.
	if first.Role == Determines {
		t.Error("a rainfall deficit must not be marked as determining the " +
			"damage figure; only the photographs do that")
	}
	if first.Role != Corroborates {
		t.Errorf("the gauge row's role is %v, want Corroborates", first.Role)
	}
	for _, want := range []string{"286.0", "516.6", "55.4"} {
		if !strings.Contains(first.Observed.EN, want) {
			t.Errorf("the gauge row omits %s: %s", want, first.Observed.EN)
		}
	}
	// And it says which unit, because the figure is not the parcel's.
	if !strings.Contains(first.Source.EN, "Barshi") ||
		!strings.Contains(first.Source.EN, "taluka") {
		t.Errorf("the gauge row does not name its unit: %s", first.Source.EN)
	}
	if !strings.Contains(first.Source.MR, "तालुका") {
		t.Errorf("the Marathi half does not name the unit: %s", first.Source.MR)
	}
}

func TestTheReanalysisTotalIsRelabelledWhenGaugesArePresent(t *testing.T) {
	s := Weather(fullWindow(), barshiGauges(), Case{Calamity: "drought"})

	var model *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "reanalysis") {
			model = &s.Rows[i]
		}
	}
	if model == nil {
		t.Fatal("the reanalysis total is not marked as modelled")
	}
	if model.Role == Determines {
		t.Error("a model total must not be marked as determining the amount")
	}
	// It leads with what it is still for -- the dry spell and the temperature
	// come from nowhere else -- and sends the officer to the gauges for the
	// amount. That ordering is the point: this row used to open by denying it
	// was a measurement, which spent its first sentence on a caveat.
	if !strings.Contains(model.Effect.EN, "Which days the rain fell on") {
		t.Errorf("the effect should open with what it is for: %s", model.Effect.EN)
	}
	if !strings.Contains(model.Effect.EN, "read the state's figure above") {
		t.Errorf("and send the officer to the gauges for the amount: %s",
			model.Effect.EN)
	}
}

func TestWithoutGaugesTheTotalKeepsItsOldNameAndSaysItIsUnchecked(t *testing.T) {
	// Most districts have no loaded rainfall, and those reports must be no
	// worse than before this source existed -- but they should not imply the
	// figure was verified.
	s := Weather(fullWindow(), nil, Case{Calamity: "drought"})

	var total *Row
	for i := range s.Rows {
		if s.Rows[i].Parameter.EN == "Total rainfall" {
			total = &s.Rows[i]
		}
	}
	if total == nil {
		t.Fatal("the total rainfall row is missing entirely")
	}
	if !strings.Contains(total.Effect.EN, "not been checked against") {
		t.Errorf("an unchecked total should say so: %s", total.Effect.EN)
	}
}

func TestAFailedMonthIsCalledOutInBothLanguages(t *testing.T) {
	// The finding a seasonal figure hides: 55% of normal reads as a poor year,
	// and August at 16% through grain fill is a crop failure.
	// With the stage known, because when a dry month fell is what decides
	// whether it cost the crop: the same shortfall in June delays sowing and in
	// September takes the grain.
	s := Weather(fullWindow(), barshiGauges(), Case{
		Calamity: "drought", StageKnown: true, PercentOfCycle: 82,
		Stage: T("दाणे भरणे", "grain filling"),
	})

	var aug *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "August") {
			aug = &s.Rows[i]
		}
	}
	if aug == nil {
		t.Fatal("the monthly rows are missing")
	}
	if !strings.Contains(aug.Reference.EN, "quarter of normal") {
		t.Errorf("August is not flagged: %s", aug.Reference.EN)
	}
	if aug.Effect.MR == "" || aug.Effect.EN == "" {
		t.Error("the month's effect is not written in both languages")
	}
	if !strings.Contains(aug.Effect.EN, "grain filling") {
		t.Errorf("it should name the stage this month fell in: %s", aug.Effect.EN)
	}

	// And September, at 31% of normal, must not get the filler line. The first
	// version flagged only months under a quarter, so the real report printed a
	// 69-point shortfall through pod development as "Monthly distribution".
	var sep *Row
	for i := range s.Rows {
		if strings.Contains(s.Rows[i].Parameter.EN, "September") {
			sep = &s.Rows[i]
		}
	}
	if sep == nil {
		t.Fatal("September is missing")
	}
	if !strings.Contains(sep.Effect.EN, "water stress") {
		t.Errorf("a month at 31%% of normal should say the crop felt it: %s",
			sep.Effect.EN)
	}
	if strings.Contains(sep.Effect.EN, "did not contribute much") {
		t.Errorf("a 69-point shortfall is called unremarkable: %s", sep.Effect.EN)
	}
}

func TestTheSeasonEffectDoesNotOverstateTheIndicator(t *testing.T) {
	// Barshi's season is 55.4% of normal, which is NOT at or below half. The
	// report must not round that into "the drought threshold is met": the
	// monthly rows carry the finding, and overstating the seasonal figure would
	// be the same error as ERA5's in the opposite direction.
	s := Weather(fullWindow(), barshiGauges(), Case{Calamity: "drought"})
	eff := s.Rows[0].Effect.EN
	if !strings.Contains(eff, "not met on this figure alone") {
		t.Errorf("the season effect overstates the indicator: %s", eff)
	}

	// Below half, it says so and says the record supports the claim.
	deficient := barshiGauges()
	deficient.Season.PercentOfNormal = 48.9
	eff = Weather(fullWindow(), deficient, Case{Calamity: "drought"}).Rows[0].Effect.EN
	if !strings.Contains(eff, "primary drought indicator") ||
		!strings.Contains(eff, "supports the claim") {
		t.Errorf("a real deficit should be stated plainly: %s", eff)
	}
	// And the officer is sent on to the question the deficit does not answer.
	//
	// This used to assert the words "not crop damage". It now asserts that the
	// row says where the answer is instead, which is the same guarantee stated
	// as a direction rather than as a denial: Barshi's rain failed and Barshi's
	// crop is green, and the officer has to be pointed at the evidence that
	// settles which happened here.
	if !strings.Contains(eff, "whether it failed on this field") {
		t.Errorf("the row should hand the officer the next question: %s", eff)
	}
	if !strings.Contains(eff, "photographs") {
		t.Errorf("and say where that question is answered: %s", eff)
	}
}

func TestGaugesAloneAreEnoughForTheSection(t *testing.T) {
	// A parcel with no position gets no reanalysis, and printing "no weather
	// record was obtained" beside a real gauge reading would read as a gap
	// where there is none.
	s := Weather(nil, barshiGauges(), Case{Calamity: "drought"})
	for _, r := range s.Rows {
		if r.Absent {
			t.Errorf("the section claims an absence: %s", r.AbsentWhy.EN)
		}
	}
	if len(s.Rows) == 0 {
		t.Error("the gauge rows should stand on their own")
	}
}
