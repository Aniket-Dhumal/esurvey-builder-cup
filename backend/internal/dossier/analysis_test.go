package dossier

import (
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/sar"
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// The Effect column does the analysis, not the documentation.
//
// A row used to explain what its parameter measures, which is the same sentence
// on every report ever printed. §8 explained that flat ground holds water on a
// report about a drought; §7 called standing water corroboration of a claim that
// the field had gone dry; §6 warned that a harvest produces the same fall in
// greenness on a crop six weeks from harvestable.
//
// The cause was structural: only Weather was told what had been claimed, so the
// other sections were handed a number and asked to explain it. With Case in hand
// they can answer the question the officer is actually asking -- what does this
// reading mean for this event, on this field, at this stage.

func drought() Case {
	return Case{
		Calamity:       "drought",
		Crop:           "Soybean",
		Stage:          T("दाणे भरणे", "grain filling"),
		PercentOfCycle: 82,
		StageKnown:     true,
		Rain:           barshiGauges(),
	}
}

func hail() Case {
	return Case{
		Calamity:       "hailstorm",
		Crop:           "Soybean",
		Stage:          T("दाणे भरणे", "grain filling"),
		PercentOfCycle: 82,
		StageKnown:     true,
	}
}

func flood() Case {
	return Case{Calamity: "flood", StageKnown: true, PercentOfCycle: 60,
		Stage: T("फुलोरा", "flowering")}
}

// --- §8 terrain -------------------------------------------------------------

func TestSlopeIsNotExplainedAsWaterloggingOnADroughtClaim(t *testing.T) {
	// The clearest case of the old behaviour: a drought report whose terrain
	// section explained the geography of standing water, an event nobody
	// alleged.
	flat := &assessment.Terrain{MeanSlopeDegrees: 1.4, Classification: "nearly level"}

	got := slopeEffect(flat, drought()).EN
	if !strings.Contains(got, "bears almost nothing") {
		t.Errorf("a drought report should say slope decides little here: %s", got)
	}
	if !strings.Contains(got, "whether rain arrived") {
		t.Errorf("and say what slope does decide instead: %s", got)
	}
	// It may mention waterlogging only as the counterfactual -- the claim that
	// would have made this figure matter.
	if strings.Contains(got, "consistent with the water damage") {
		t.Errorf("a drought report asserts consistency with water damage: %s", got)
	}

	// The same ground under a flood claim reads the other way.
	got = slopeEffect(flat, flood()).EN
	if !strings.Contains(got, "nowhere to run") ||
		!strings.Contains(got, "consistent with the water damage claimed") {
		t.Errorf("a flood claim on level ground should say the ground fits: %s", got)
	}
}

func TestSlopeAgainstAFloodClaimOnASlopeSaysCheckItRatherThanRefuteIt(t *testing.T) {
	steep := &assessment.Terrain{MeanSlopeDegrees: 8.2, Classification: "moderate"}
	got := slopeEffect(steep, flood()).EN

	if !strings.Contains(got, "worth checking") {
		t.Errorf("it should send the officer to look: %s", got)
	}
	if !strings.Contains(got, "does not refute") {
		t.Errorf("and not let the slope defeat the claim -- water collects behind "+
			"a bund: %s", got)
	}
}

func TestSlopeOnAHailClaimSaysItBearsNothing(t *testing.T) {
	// Hail does not care about slope, and pretending otherwise fills the one
	// sentence an officer reads with nothing.
	got := slopeEffect(&assessment.Terrain{MeanSlopeDegrees: 1.4}, hail()).EN
	if !strings.Contains(got, "does not depend on slope") {
		t.Errorf("%s", got)
	}
	// It still says why the figure is recorded at all.
	if !strings.Contains(got, "later claim") {
		t.Errorf("it should say why this is recorded anyway: %s", got)
	}
}

func TestAnUnfamiliarCalamityGetsADescriptionAndNoAssertion(t *testing.T) {
	// The calamity vocabulary is the state's and it grows. An unrecognised one
	// must not be silently treated as a flood.
	odd := Case{Calamity: "locust attack"}
	got := slopeEffect(&assessment.Terrain{MeanSlopeDegrees: 1.4,
		Classification: "nearly level"}, odd).EN

	if !strings.Contains(got, "governs how quickly water leaves") {
		t.Errorf("it should describe what slope governs: %s", got)
	}
	for _, mustNot := range []string{"consistent with", "does not refute"} {
		if strings.Contains(got, mustNot) {
			t.Errorf("it asserts something about an unknown calamity (%q): %s",
				mustNot, got)
		}
	}
}

func TestWaterloggingSusceptibilityFindsItsOneBearingOnADrought(t *testing.T) {
	// Rather than saying it bears nothing, the honest reading: ground that
	// holds water also holds soil moisture, so a dry spell reaches the crop
	// later here. That is analysis; "not a measure of loss" was not.
	got := waterloggingEffect(
		&assessment.Terrain{WaterloggingRisk: "High"}, drought()).EN

	if !strings.Contains(got, "holds soil moisture") {
		t.Errorf("the indirect bearing is the useful part: %s", got)
	}
	if !strings.Contains(got, "reaches the crop later") {
		t.Errorf("and it should say which way it cuts: %s", got)
	}
}

// --- §7 radar ---------------------------------------------------------------

func TestStandingWaterOnADroughtClaimIsAContradictionNotCorroboration(t *testing.T) {
	// The bug. This row used to read "Standing water strengthens the claim"
	// whatever had been claimed, so a radar finding that flatly contradicts a
	// drought was printed as evidence for it.
	got := radarEffect(sar.FindingInundation, drought()).EN

	if strings.Contains(got, "strengthens the claim") ||
		strings.Contains(got, "the two agree") {
		t.Fatalf("water found on a field claimed to have gone dry is called "+
			"corroboration: %s", got)
	}
	if !strings.Contains(got, "contradict") {
		t.Errorf("it should say the two contradict each other: %s", got)
	}
	// And still offer the innocent readings, because radar is not the last word.
	if !strings.Contains(got, "pass may post-date") {
		t.Errorf("the timing explanation should be offered: %s", got)
	}
	if !strings.Contains(got, "wet smooth ground") {
		t.Errorf("and the instrument's own ambiguity: %s", got)
	}
}

func TestStandingWaterOnAFloodClaimStillReadsAsAgreement(t *testing.T) {
	got := radarEffect(sar.FindingInundation, flood()).EN
	if !strings.Contains(got, "the two agree") {
		t.Errorf("water found on a water claim is agreement: %s", got)
	}
}

func TestNoChangeMeansLessOnADroughtThanOnAHailstorm(t *testing.T) {
	// Radar measures surface roughness. Hail roughens a field; a crop drying
	// under water stress does not. So "no change" is near-uninformative for a
	// drought and was being reported as disagreement with the claim.
	dry := radarEffect(sar.FindingNoChange, drought()).EN
	if !strings.Contains(dry, "does not contradict the claim") {
		t.Errorf("no-change should not be read as contradicting a drought: %s", dry)
	}
	if !strings.Contains(dry, "surface roughness") {
		t.Errorf("and should say why radar is the wrong instrument here: %s", dry)
	}
	if !strings.Contains(dry, "optical record") {
		t.Errorf("and point at the instrument that can answer: %s", dry)
	}

	wind := radarEffect(sar.FindingNoChange, hail()).EN
	if !strings.Contains(wind, "does not agree with the claim") {
		t.Errorf("for hail, an unchanged surface is a real disagreement: %s", wind)
	}
}

func TestLodgingFoundWithoutAWindClaimIsNotCalledAgreement(t *testing.T) {
	got := radarEffect(sar.FindingLodging, drought()).EN
	if strings.Contains(got, "two independent sources agree") {
		t.Errorf("lodging is not agreement with a drought claim: %s", got)
	}
	if !strings.Contains(got, "the canopy went down") {
		t.Errorf("it should say what the finding does establish: %s", got)
	}

	if !strings.Contains(radarEffect(sar.FindingLodging, hail()).EN,
		"two independent sources agree") {
		t.Error("for hail, radar and the photographs corroborating each other " +
			"is the point")
	}
}

// --- §6 optical -------------------------------------------------------------

func TestTheHarvestAlternativeFollowsTheCalendar(t *testing.T) {
	// A harvest is the standard alternative explanation for a collapse in
	// canopy, and it is only available once there is something to harvest.
	// Warning about it on a crop a third of the way through its cycle sends an
	// officer after an explanation the calendar rules out, and invites them to
	// doubt a sound claim.
	sat := fullSatellite()

	late := rrvlEffect(sat, drought()).EN // 82% of cycle
	if !strings.Contains(late, "it was harvestable") {
		t.Errorf("at 82%% of the cycle a harvest is available: %s", late)
	}
	if !strings.Contains(late, "an early harvest produces exactly this fall") {
		t.Errorf("and must be offered: %s", late)
	}

	early := drought()
	early.PercentOfCycle = 35
	early.Stage = T("वाढीची अवस्था", "vegetative growth")
	got := rrvlEffect(sat, early).EN

	if !strings.Contains(got, "nothing to harvest") {
		t.Errorf("at 35%% of the cycle a harvest is ruled out: %s", got)
	}
	if !strings.Contains(got, "ruled out by the calendar") {
		t.Errorf("and the report should say so plainly: %s", got)
	}
	// And say what is left, rather than stopping at the exclusion.
	if !strings.Contains(got, "water stress") {
		t.Errorf("it should name the remaining explanations: %s", got)
	}
}

func TestWithNoStageTheHarvestCaveatStands(t *testing.T) {
	// Nothing is ruled out by a calendar nobody could read.
	c := drought()
	c.StageKnown, c.PercentOfCycle = false, 0
	got := rrvlEffect(fullSatellite(), c).EN

	if !strings.Contains(got, "cannot be ruled out") {
		t.Errorf("%s", got)
	}
	if strings.Contains(got, "nothing to harvest") {
		t.Errorf("an unknown stage must not be read as an early one: %s", got)
	}
}

func TestTheFallAlwaysSaysWhatItIsAFallFrom(t *testing.T) {
	// Whatever branch follows, the officer is told the figure is the field
	// against its own peak -- not against its neighbours, which is the other
	// standard misreading of a relative index.
	for _, c := range []Case{drought(), hail(), flood(), {}} {
		got := rrvlEffect(fullSatellite(), c).EN
		if !strings.Contains(got, "at its own peak") {
			t.Errorf("calamity %q: %s", c.Calamity, got)
		}
	}
}

func TestADroughtWithFailedRainSaysTheTwoReadingsAgree(t *testing.T) {
	// Two instruments pointing at one event is worth saying out loud; it is the
	// thing an officer is trying to establish.
	got := rrvlEffect(fullSatellite(), drought()).EN
	if !strings.Contains(got, "point at the same event") {
		t.Errorf("the canopy fall and the rainfall deficit agree: %s", got)
	}

	// With a normal season the same canopy fall means something else entirely,
	// and the sentence must not appear.
	c := drought()
	c.Rain = barshiGauges()
	c.Rain.Season.PercentOfNormal = 96
	if strings.Contains(rrvlEffect(fullSatellite(), c).EN, "point at the same event") {
		t.Error("a normal season must not be reported as agreeing with a drought")
	}
}

// --- §5 weather -------------------------------------------------------------

func TestTheDrySpellIsReadAgainstTheGrowthStage(t *testing.T) {
	// "The figure a drought claim turns on" is true of the parameter and says
	// nothing about the fifteen days in front of the officer.
	w := fullWindow()
	w.LongestDrySpell, w.LongestDrySpellFrom = 15, "2026-09-05"

	got := drySpellEffect(w, drought()).EN
	for _, want := range []string{"15 days", "2026-09-05", "grain filling", "costs yield"} {
		if !strings.Contains(got, want) {
			t.Errorf("the dry-spell analysis omits %q: %s", want, got)
		}
	}
}

func TestAShortDrySpellOnADroughtClaimSaysSoAndRedirects(t *testing.T) {
	// The honest reading when the reanalysis shows no long break: the seasonal
	// shortfall may still be real, in the state's monthly figures.
	w := fullWindow()
	w.LongestDrySpell = 4

	got := drySpellEffect(w, drought()).EN
	if !strings.Contains(got, "shorter than a drought claim would ordinarily") {
		t.Errorf("%s", got)
	}
	if !strings.Contains(got, "several short breaks") {
		t.Errorf("and should redirect to the monthly figures: %s", got)
	}
}

func TestADrySpellBeforeAFloodClaimIsAboutAttribution(t *testing.T) {
	// Not part of the claimed event, and it bears on it: a crop already dry
	// when the water arrived did not lose everything to the water.
	w := fullWindow()
	w.LongestDrySpell, w.LongestDrySpellFrom = 18, "2026-07-02"

	got := drySpellEffect(w, flood()).EN
	if !strings.Contains(got, "not part of that event") {
		t.Errorf("%s", got)
	}
	if !strings.Contains(got, "did not lose everything to the water") {
		t.Errorf("the attribution point is the useful one: %s", got)
	}
}

// --- §4 field measurement ---------------------------------------------------

func TestLodgingOnADroughtClaimIsFlaggedWithoutAccusing(t *testing.T) {
	// Water stress withers a crop; it does not flatten one. So lodging on a
	// drought claim means something happened that the claim does not describe --
	// which is worth an officer's attention and is not evidence of dishonesty.
	got := lodgingEffect(35, drought()).EN

	if !strings.Contains(got, "does not flatten a crop") {
		t.Errorf("the mismatch should be stated: %s", got)
	}
	if !strings.Contains(got, "not against the farmer") {
		t.Errorf("and must not read as an accusation: %s", got)
	}
	if !strings.Contains(got, "may itself be compensable") {
		t.Errorf("a real loss under the wrong heading is still a real loss: %s", got)
	}
	// The arithmetic rule survives in every branch.
	if !strings.Contains(got, "kept separate from stand loss") {
		t.Errorf("the double-counting rule was dropped: %s", got)
	}
}

func TestLodgingOnAHailClaimIsDirectEvidence(t *testing.T) {
	got := lodgingEffect(35, hail()).EN
	if !strings.Contains(got, "direct evidence of it") {
		t.Errorf("%s", got)
	}
	if !strings.Contains(got, "kept separate from stand loss") {
		t.Errorf("the double-counting rule was dropped: %s", got)
	}
}

// --- the property that makes all of this worth having -----------------------

func TestTheAnalysisActuallyChangesWithTheClaim(t *testing.T) {
	// The whole point. If a section produces the same prose for a drought and a
	// hailstorm, it is documenting a parameter rather than analysing a case, and
	// the officer is back where they started.
	flat := &assessment.Terrain{MeanSlopeDegrees: 1.4, Classification: "nearly level"}
	w := fullWindow()
	w.LongestDrySpell, w.LongestDrySpellFrom = 15, "2026-09-05"

	for name, pair := range map[string][2]string{
		"slope": {
			slopeEffect(flat, drought()).EN,
			slopeEffect(flat, flood()).EN,
		},
		"waterlogging": {
			waterloggingEffect(&assessment.Terrain{WaterloggingRisk: "High"}, drought()).EN,
			waterloggingEffect(&assessment.Terrain{WaterloggingRisk: "High"}, flood()).EN,
		},
		"radar inundation": {
			radarEffect(sar.FindingInundation, drought()).EN,
			radarEffect(sar.FindingInundation, flood()).EN,
		},
		"radar no change": {
			radarEffect(sar.FindingNoChange, drought()).EN,
			radarEffect(sar.FindingNoChange, hail()).EN,
		},
		"dry spell": {
			drySpellEffect(w, drought()).EN,
			drySpellEffect(w, flood()).EN,
		},
		"lodging": {
			lodgingEffect(35, drought()).EN,
			lodgingEffect(35, hail()).EN,
		},
	} {
		if pair[0] == pair[1] {
			t.Errorf("%s reads identically for two different claims, so it is "+
				"documenting the parameter rather than analysing the case", name)
		}
		for i, half := range pair {
			if strings.TrimSpace(half) == "" {
				t.Errorf("%s produced nothing for branch %d", name, i)
			}
		}
	}
}

func TestEveryBranchIsWrittenInBothLanguages(t *testing.T) {
	// A branch somebody added in English only prints English inside the Marathi
	// report, which is the half the farmer reads.
	cases := []Case{drought(), hail(), flood(), {Calamity: "locust attack"}, {}}
	flat := &assessment.Terrain{MeanSlopeDegrees: 1.4, Classification: "level"}
	steep := &assessment.Terrain{MeanSlopeDegrees: 9, Classification: "moderate"}
	w := fullWindow()

	for _, c := range cases {
		for _, spell := range []int{3, 15} {
			w.LongestDrySpell, w.LongestDrySpellFrom = spell, "2026-09-05"
			check(t, "dry spell", c, drySpellEffect(w, c))
		}
		for _, ter := range []*assessment.Terrain{flat, steep} {
			check(t, "slope", c, slopeEffect(ter, c))
		}
		check(t, "waterlogging", c,
			waterloggingEffect(&assessment.Terrain{WaterloggingRisk: "High"}, c))
		check(t, "lodging", c, lodgingEffect(35, c))
		check(t, "rrvl", c, rrvlEffect(fullSatellite(), c))
		for _, code := range []string{
			sar.FindingInundation, sar.FindingLodging,
			sar.FindingCanopyLoss, sar.FindingNoChange,
		} {
			check(t, "radar "+code, c, radarEffect(code, c))
		}
	}
}

func check(t *testing.T, what string, c Case, got Text) {
	t.Helper()
	if strings.TrimSpace(got.EN) == "" {
		t.Errorf("%s (%q): no English", what, c.Calamity)
	}
	if strings.TrimSpace(got.MR) == "" {
		t.Errorf("%s (%q): no Marathi", what, c.Calamity)
	}
	// A Marathi half that is only the English one is an untranslated branch.
	if got.MR == got.EN && len(got.EN) > 40 {
		t.Errorf("%s (%q): the Marathi half is the English one", what, c.Calamity)
	}
}

// --- the rows the real report exposed --------------------------------------

func TestAMonthWellShortOfNormalIsNotCalledMonthlyDistribution(t *testing.T) {
	// Found by rendering the real claim. September was 51 mm of a normal 167 --
	// a 69-point shortfall through pod development -- and the report printed
	// "Monthly distribution. A season total does not show whether the rain
	// arrived when the crop needed it." The threshold flagged only months under
	// a quarter, so the second-worst month of a declared drought got the filler
	// line.
	c := drought()
	for _, tc := range []struct {
		pct  float64
		want string
	}{
		{15.6, "no water to speak of"},
		{30.7, "felt water stress"},
		{48.0, "felt water stress"},
		{76.0, "did not contribute much"},
		{108.8, "normal rainfall or more"},
	} {
		got := monthEffect(weather.OfficialPeriod{
			Label: "September 2026", PercentOfNormal: tc.pct}, c).EN
		if !strings.Contains(got, tc.want) {
			t.Errorf("%.1f%% of normal should say %q, got: %s", tc.pct, tc.want, got)
		}
		// And the stage, because when it fell is what decides whether it cost
		// the crop.
		if !strings.Contains(got, "grain filling") {
			t.Errorf("%.1f%%: the stage is not named: %s", tc.pct, got)
		}
	}
}

func TestTheWettestDayIsReadForTheClaimItBearsOn(t *testing.T) {
	// "For a heavy-rain claim this matters more than the total" was printed on
	// a drought report, where the wettest day of the season bears on the claim
	// hardly at all. What it does bear on there is whether the dry spell was
	// ever broken.
	w := fullWindow()
	w.MaxDailyMM, w.MaxDailyOn = 35, "2026-07-07"

	dry := heaviestDayEffect(w, drought()).EN
	if !strings.Contains(dry, "when, if ever, the dry spell was") {
		t.Errorf("on a drought claim: %s", dry)
	}
	if strings.Contains(dry, "matters more than the total") {
		t.Errorf("a drought report is told this matters more than the total: %s", dry)
	}

	wet := heaviestDayEffect(w, flood()).EN
	if !strings.Contains(wet, "matters more than the total") {
		t.Errorf("on a water claim it genuinely does: %s", wet)
	}
	if !strings.Contains(wet, "check the date") {
		t.Errorf("and the date is the thing to check: %s", wet)
	}
}

func TestTheGustIsNotOfferedAsALodgingExplanationOnADrought(t *testing.T) {
	// "The main cause of lodging" on a drought report invites an officer to look
	// for lodging nobody alleged.
	w := fullWindow()
	w.MaxGustKmh, w.MaxGustOn = 62, "2026-07-24"

	dry := gustEffect(w, drought()).EN
	if !strings.Contains(dry, "bears nothing on a drought claim") {
		t.Errorf("%s", dry)
	}
	// It still says why it is recorded, which is the honest reason.
	if !strings.Contains(dry, "if the photographs show") {
		t.Errorf("it should say what it would be for: %s", dry)
	}

	strong := gustEffect(w, hail()).EN
	if !strings.Contains(strong, "direct evidence of it") {
		t.Errorf("62 km/h on a hail claim is evidence: %s", strong)
	}

	// A gust too weak to flatten a crop, on a wind claim, must not defeat it:
	// the record is an 11 km average and cannot see a downburst.
	w.MaxGustKmh = 28
	weak := gustEffect(w, hail()).EN
	if !strings.Contains(weak, "does not refute the claim") {
		t.Errorf("a weak gust must not refute a wind claim: %s", weak)
	}
	if !strings.Contains(weak, "local downburst") {
		t.Errorf("and should say why the record cannot see it: %s", weak)
	}
}
