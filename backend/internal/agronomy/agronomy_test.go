package agronomy

import (
	"fmt"
	"math"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The crop table
// ---------------------------------------------------------------------------

func TestEveryCropIsInternallyConsistent(t *testing.T) {
	// A table is only worth having if it cannot quietly contradict itself. A
	// stage that starts before the previous one ended, or a duration whose
	// maturity sits outside the stage range, would place crops wrongly for
	// every assessment of that crop and nothing would say so.
	for _, c := range All() {
		t.Run(c.Key, func(t *testing.T) {
			if len(c.Stages) < 3 {
				t.Fatalf("%d stages; a crop needs at least establishment, "+
					"reproductive and maturity to be placed", len(c.Stages))
			}
			if c.BaseTempC <= 0 {
				t.Error("no base temperature; GDD would be meaningless")
			}
			if c.CutoffTempC <= c.BaseTempC {
				t.Errorf("cutoff %.1f is not above base %.1f", c.CutoffTempC, c.BaseTempC)
			}

			prev := c.Stages[0]
			if prev.FromGDD != 0 {
				t.Errorf("the first stage starts at %.0f GDD, not 0", prev.FromGDD)
			}
			for i, s := range c.Stages {
				if s.ToGDD <= s.FromGDD {
					t.Errorf("stage %s spans %.0f to %.0f", s.Key, s.FromGDD, s.ToGDD)
				}
				if i > 0 && s.FromGDD != c.Stages[i-1].ToGDD {
					t.Errorf("gap or overlap between %s (ends %.0f) and %s (starts %.0f)",
						c.Stages[i-1].Key, c.Stages[i-1].ToGDD, s.Key, s.FromGDD)
				}
				if s.Class() == "" {
					t.Errorf("stage %s has no class, so no susceptibility can be "+
						"looked up for it", s.Key)
				}
			}

			if len(c.Durations) == 0 {
				t.Error("no duration classes")
			}
			for _, d := range c.Durations {
				if d.DaysMin <= 0 || d.DaysMax < d.DaysMin {
					t.Errorf("duration %s spans %d to %d days", d.Key, d.DaysMin, d.DaysMax)
				}
				if d.MaturityG <= 0 {
					t.Errorf("duration %s has no maturity GDD", d.Key)
				}
			}
			if len(c.Seasons) == 0 {
				t.Error("no seasons; the crop cannot be sanity-checked against a report")
			}
			if c.Provenance == "" {
				t.Error("no provenance; a figure with no source cannot be defended")
			}
		})
	}
}

func TestACropWhoseYieldPassesThroughAFlowerHasAFloweringStage(t *testing.T) {
	// Where the yield passes through a flower, damage there is permanent and
	// the stage must exist for the matrix to time damage against.
	//
	// Where it does not, it must not. Sugarcane is cut as stem and onion
	// lifted as bulb: both go from vegetative growth straight to filling what
	// is sold. Inventing a flowering stage for them would be modelling a
	// plant that is not in the field.
	for _, c := range All() {
		var found bool
		for _, s := range c.Stages {
			if s.Class() == ClassReproductive {
				found = true
			}
		}
		if c.FlowersForYield() && !found {
			t.Errorf("%s is harvested as %s but has no flowering stage",
				c.Key, c.HarvestedOrgan)
		}
		if c.HarvestedOrgan == "" {
			t.Errorf("%s does not say what is harvested from it", c.Key)
		}
	}
}

func TestEveryCropReachesTheStageItsYieldIsMadeIn(t *testing.T) {
	// Whatever the organ, there is a stage where it fills. Without one the
	// matrix has nothing to attach the costly part of the season to.
	for _, c := range All() {
		var found bool
		for _, s := range c.Stages {
			if s.Class() == ClassFill {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no stage in which its yield is made", c.Key)
		}
	}
}

func TestRecoverabilityOnlyEverGoesOneWay(t *testing.T) {
	// A crop does not become able to make up damage again after it has
	// stopped being able to. If a table ever said so it would be a typo, and
	// the consequence would be understating a late-season loss.
	for _, c := range All() {
		var seenPermanent bool
		for _, s := range c.Stages {
			if !s.Recoverable {
				seenPermanent = true
				continue
			}
			if seenPermanent {
				t.Errorf("%s: stage %s is recoverable after an earlier stage was not",
					c.Key, s.Key)
			}
		}
	}
}

func TestACropIsFoundByTheNamesItIsActuallyFiledUnder(t *testing.T) {
	// One crop arrives spelled four ways from four systems. A lookup that
	// only matched the canonical spelling would drop the stage reasoning for
	// most real rows, silently.
	cases := map[string]string{
		"Soybean":   "soybean",
		"soyabean":  "soybean",
		"SOYA BEAN": "soybean",
		"सोयाबीन":   "soybean",
		"tur":       "pigeonpea",
		"Toor":      "pigeonpea",
		"तूर":       "pigeonpea",
		"arhar":     "pigeonpea",
		"jowar":     "sorghum",
		"ज्वारी":    "sorghum",
		"bajra":     "pearl_millet",
		"Paddy":     "rice",
		"भात":       "rice",
		"gram":      "chickpea",
		"harbhara":  "chickpea",
		"कापूс":     "", // deliberately misspelled Marathi: must not match
	}
	for name, want := range cases {
		got, ok := Find(name)
		if want == "" {
			if ok {
				t.Errorf("%q matched %s; a near-miss spelling should not resolve",
					name, got.Key)
			}
			continue
		}
		if !ok {
			t.Errorf("%q matched nothing, want %s", name, want)
			continue
		}
		if got.Key != want {
			t.Errorf("%q matched %s, want %s", name, got.Key, want)
		}
	}
}

func TestAnUnknownCropIsNotGuessedAt(t *testing.T) {
	for _, name := range []string{"", "   ", "quinoa", "dragon fruit"} {
		if c, ok := Find(name); ok {
			t.Errorf("%q resolved to %s", name, c.Key)
		}
	}
}

func TestStageAtWalksTheWholeCycle(t *testing.T) {
	c, ok := Find("soybean")
	if !ok {
		t.Fatal("soybean is missing")
	}
	cases := []struct {
		gdd  float64
		want string
	}{
		{0, "emergence"},
		{119, "emergence"},
		{120, "vegetative"},
		{649, "vegetative"},
		{650, "flowering"},
		{999, "flowering"},
		{1000, "pod_development"},
		{1400, "seed_fill"},
		{1750, "maturity"},
		{5000, "maturity"}, // past maturity: a standing crop, not an error
	}
	for _, tc := range cases {
		s, ok := c.StageAt(tc.gdd)
		if !ok {
			t.Errorf("%.0f GDD placed nowhere", tc.gdd)
			continue
		}
		if s.Key != tc.want {
			t.Errorf("%.0f GDD = %s, want %s", tc.gdd, s.Key, tc.want)
		}
	}
}

func TestAnUndeclaredVarietyFallsBackToMediumAndSaysSo(t *testing.T) {
	c, _ := Find("soybean")

	d, matched := c.DurationFor("short")
	if !matched || d.Key != "short" {
		t.Errorf("a declared duration was not honoured: %+v matched=%v", d, matched)
	}

	d, matched = c.DurationFor("")
	if matched {
		t.Error("an empty duration was reported as a match")
	}
	if d.Key != "medium" {
		t.Errorf("fell back to %s, want medium", d.Key)
	}
}

// ---------------------------------------------------------------------------
// Thermal time
// ---------------------------------------------------------------------------

func TestADayContributesTheHeatThePlantCouldUse(t *testing.T) {
	c, _ := Find("soybean") // base 10, cutoff 30

	cases := []struct {
		name     string
		max, min float64
		want     float64
	}{
		{"an ordinary day", 30, 20, 15},              // (30+20)/2 - 10
		{"a cold night does not subtract", 20, 5, 5}, // min lifted to base: (20+10)/2-10
		{"a day entirely below base", 8, 4, 0},
		{"a scorching day is capped", 44, 28, 19}, // max capped to 30: (30+28)/2-10
		{"exactly at base", 10, 10, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DailyGDD(c, Day{Date: "2026-07-01", TMaxC: tc.max, TMinC: tc.min})
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("DailyGDD = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestTheUpperCutoffIsWhatStopsAVidarbhaMayRunningTheCropAhead(t *testing.T) {
	// Without it, a month of 44 °C days accumulates heat the plant never
	// used, and the crop is placed weeks past where it stands in the field.
	c, _ := Find("soybean")
	hot := Day{Date: "2026-05-01", TMaxC: 44, TMinC: 30}

	capped := DailyGDD(c, hot)
	uncapped := (44+30)/2 - c.BaseTempC

	if capped >= uncapped {
		t.Errorf("capped %v is not below uncapped %v", capped, uncapped)
	}
}

// season builds a run of identical days from a start date.
func season(from string, n int, max, min float64) []Day {
	start, _ := time.Parse(dayLayout, from)
	out := make([]Day, n)
	for i := range out {
		out[i] = Day{
			Date:  start.AddDate(0, 0, i).Format(dayLayout),
			TMaxC: max, TMinC: min,
		}
	}
	return out
}

func TestACropIsPlacedByHeatNotByTheCalendar(t *testing.T) {
	// The whole reason this file exists. The same variety, the same number of
	// days, two different seasons -- and two different stages.
	c, _ := Find("soybean")

	warm := season("2026-06-15", 60, 34, 24) // capped mean 27 -> 17 GDD/day
	cool := season("2026-06-15", 60, 24, 16) // mean 20 -> 10 GDD/day

	hot, err := StageOn(c, "medium", warm, "2026-06-15", "2026-08-13")
	if err != nil {
		t.Fatal(err)
	}
	cold, err := StageOn(c, "medium", cool, "2026-06-15", "2026-08-13")
	if err != nil {
		t.Fatal(err)
	}

	if hot.CalendarDays != cold.CalendarDays {
		t.Fatalf("the two runs are not the same length: %d vs %d",
			hot.CalendarDays, cold.CalendarDays)
	}
	if hot.AccumulatedGDD <= cold.AccumulatedGDD {
		t.Errorf("the warm season accumulated %v, the cool one %v",
			hot.AccumulatedGDD, cold.AccumulatedGDD)
	}
	if hot.Stage == cold.Stage {
		t.Errorf("both seasons placed the crop at %s; heat made no difference",
			hot.Stage)
	}
	if !hot.Recoverable == !cold.Recoverable {
		t.Logf("warm=%s cold=%s", hot.Stage, cold.Stage)
	}
}

func TestAShortDurationVarietyIsFurtherThroughItsCycle(t *testing.T) {
	// The same heat, two varieties. The short one is nearer maturity, and if
	// the table ignored that it would assess an early crop as a late one.
	c, _ := Find("soybean")
	days := season("2026-06-15", 70, 32, 22)

	short, err := StageOn(c, "short", days, "2026-06-15", "2026-08-23")
	if err != nil {
		t.Fatal(err)
	}
	long, err := StageOn(c, "long", days, "2026-06-15", "2026-08-23")
	if err != nil {
		t.Fatal(err)
	}

	if short.AccumulatedGDD != long.AccumulatedGDD {
		t.Fatalf("the accumulation itself differed: %v vs %v",
			short.AccumulatedGDD, long.AccumulatedGDD)
	}
	if short.PercentOfCycle <= long.PercentOfCycle {
		t.Errorf("short is %.1f%% through, long is %.1f%%; short should be further",
			short.PercentOfCycle, long.PercentOfCycle)
	}
}

func TestMissingTemperatureDaysAreReportedNotInterpolated(t *testing.T) {
	// A gap makes the crop read early. Interpolating would hide that; saying
	// so lets the reader discount the stage by the right direction.
	c, _ := Find("soybean")
	full := season("2026-06-15", 40, 32, 22)
	sparse := full[:25]

	got, err := StageOn(c, "medium", sparse, "2026-06-15", "2026-07-24")
	if err != nil {
		t.Fatal(err)
	}
	if got.DaysMissing != 15 {
		t.Errorf("missing = %d, want 15", got.DaysMissing)
	}
	if got.Confidence != ConfidenceLow {
		t.Errorf("confidence = %s with a third of the season absent", got.Confidence)
	}
	if len(got.Caveats) == 0 {
		t.Error("no caveat was recorded for a series with a gap")
	}
}

func TestAnUndeclaredVarietyIsFlaggedOnTheFigure(t *testing.T) {
	c, _ := Find("soybean")
	got, err := StageOn(c, "", season("2026-06-15", 30, 30, 20), "2026-06-15", "2026-07-14")
	if err != nil {
		t.Fatal(err)
	}
	if !got.DurationAssumed {
		t.Error("an assumed duration was not marked as assumed")
	}
	var said bool
	for _, c := range got.Caveats {
		if len(c) > 0 && (c[0] == 't') {
			said = true
		}
	}
	if !said {
		t.Errorf("no caveat mentions the assumption: %v", got.Caveats)
	}
}

func TestAnIncidentBeforeSowingIsRefused(t *testing.T) {
	c, _ := Find("soybean")
	_, err := StageOn(c, "medium", season("2026-06-15", 10, 30, 20),
		"2026-06-15", "2026-06-01")
	if err == nil {
		t.Fatal("a date before sowing was accepted")
	}
}

func TestHeatDaysDuringFloweringAreCountedSeparately(t *testing.T) {
	// A crop that went through a week above its threshold at flowering has
	// already lost yield, whatever the calamity claimed. Counting those days
	// among all hot days would bury the fact.
	c, _ := Find("soybean") // heat threshold 35

	// Warm enough to reach flowering, with the late days above threshold.
	days := season("2026-06-15", 45, 34, 24)
	for i := 40; i < 45; i++ {
		days[i].TMaxC = 39
	}

	s, err := StressOver(c, "medium", days, "2026-06-15", "2026-06-15", "2026-07-29")
	if err != nil {
		t.Fatal(err)
	}
	if s.HeatDays != 5 {
		t.Errorf("heat days = %d, want 5", s.HeatDays)
	}
	if s.HottestC != 39 {
		t.Errorf("hottest = %v, want 39", s.HottestC)
	}
	if s.HeatDaysAtFlowering == 0 {
		t.Error("none of the hot days were attributed to flowering, though the " +
			"crop reached it")
	}
}

func TestNoHeatClaimIsMadeForACropWithNoThreshold(t *testing.T) {
	c, _ := Find("sugarcane") // no HeatStressC in the table
	s, err := StressOver(c, "medium", season("2026-04-01", 20, 44, 30),
		"2026-01-01", "2026-04-01", "2026-04-20")
	if err != nil {
		t.Fatal(err)
	}
	if s.HeatDays != 0 {
		t.Errorf("heat days = %d for a crop with no established threshold", s.HeatDays)
	}
	if len(s.Notes) == 0 {
		t.Error("nothing said that no threshold is established")
	}
}

func TestStressOverAnEmptyWindowIsAnError(t *testing.T) {
	c, _ := Find("soybean")
	_, err := StressOver(c, "medium", season("2026-06-15", 10, 30, 20),
		"2026-06-15", "2026-09-01", "2026-09-10")
	if err == nil {
		t.Fatal("a window with no records reported stress anyway")
	}
}

// ---------------------------------------------------------------------------
// Susceptibility
// ---------------------------------------------------------------------------

func TestHailCostsMoreAtPodFillThanAtSeedling(t *testing.T) {
	// The single thing this matrix exists to say.
	c, _ := Find("soybean")

	early, ok := SusceptibilityFor(c, "vegetative", "hailstorm")
	if !ok {
		t.Fatal("no reading for vegetative hail")
	}
	late, ok := SusceptibilityFor(c, "seed_fill", "hailstorm")
	if !ok {
		t.Fatal("no reading for seed-fill hail")
	}

	if rank(early.Level) >= rank(late.Level) {
		t.Errorf("vegetative hail is %s and seed-fill hail is %s", early.Level, late.Level)
	}
	if !early.Recoverable {
		t.Error("vegetative damage is reported as unrecoverable")
	}
	if late.Recoverable {
		t.Error("seed-fill damage is reported as recoverable")
	}
	if early.Reason == "" || late.Reason == "" {
		t.Error("a level with no reason cannot be shown to anybody")
	}
}

func TestDroughtAtMaturityIsNotADroughtLoss(t *testing.T) {
	// A crop that has already made its yield does not lose it to a dry spell.
	// Getting this backwards would pay for losses that did not happen.
	c, _ := Find("sorghum")
	got, ok := SusceptibilityFor(c, "maturity", "drought")
	if !ok {
		t.Fatal("no reading")
	}
	if got.Level != SeverityLow {
		t.Errorf("drought at maturity = %s, want low", got.Level)
	}
}

func TestCottonCanPartlyMakeUpEarlyBollLossAndTheTableSaysSo(t *testing.T) {
	// The exception that justifies having exceptions: an indeterminate crop
	// re-flowers where a determinate one cannot.
	cotton, _ := Find("cotton")
	soy, _ := Find("soybean")

	c, ok := SusceptibilityFor(cotton, "boll_development", "hailstorm")
	if !ok {
		t.Fatal("no cotton reading")
	}
	s, ok := SusceptibilityFor(soy, "seed_fill", "hailstorm")
	if !ok {
		t.Fatal("no soybean reading")
	}

	if rank(c.Level) >= rank(s.Level) {
		t.Errorf("cotton at boll fill is %s and soybean at seed fill is %s; "+
			"the indeterminate habit is not reflected", c.Level, s.Level)
	}
	if c.Reason == s.Reason {
		t.Error("the exception carries the generic reason")
	}
}

func TestAnUnknownCombinationSaysSoRatherThanGuessing(t *testing.T) {
	// A stated "moderate" that came from nowhere is worse than an admitted
	// gap: it reads as a finding.
	c, _ := Find("soybean")

	if _, ok := SusceptibilityFor(c, "flowering", "volcano"); ok {
		t.Error("an unmodelled calamity returned a reading")
	}
	if _, ok := SusceptibilityFor(c, "not_a_stage", "hailstorm"); ok {
		t.Error("an unmodelled stage returned a reading")
	}
	if _, ok := SusceptibilityFor(nil, "flowering", "hailstorm"); ok {
		t.Error("a nil crop returned a reading")
	}
}

func TestEveryCalamityTheFormOffersHasEveryStageClass(t *testing.T) {
	// The form's calamity list and this matrix must not drift apart: a
	// calamity a farmer can select and the matrix has no row for produces an
	// assessment with no susceptibility reasoning at all.
	classes := []string{
		ClassEstablishment, ClassVegetative,
		ClassReproductive, ClassFill, ClassMaturity,
	}
	for _, cal := range Calamities() {
		for _, cl := range classes {
			if _, ok := matrix[cal][cl]; !ok {
				t.Errorf("%s has no row for %s", cal, cl)
			}
		}
	}
}

func TestEveryReadingCarriesItsProvenance(t *testing.T) {
	c, _ := Find("soybean")
	got, _ := SusceptibilityFor(c, "flowering", "hailstorm")
	if got.Provenance != ProvenancePublished {
		t.Errorf("provenance = %q; a figure with no stated source cannot be "+
			"defended in an appeal", got.Provenance)
	}
}

func rank(level string) int {
	switch level {
	case SeverityLow:
		return 1
	case SeverityModerate:
		return 2
	case SeverityHigh:
		return 3
	case SeveritySevere:
		return 4
	}
	return 0
}

// ---------------------------------------------------------------------------
// Green-up
// ---------------------------------------------------------------------------

// curve builds an NDVI series from a start date at a fixed interval.
func curve(from string, everyDays int, values ...float64) []Observation {
	start, _ := time.Parse(dayLayout, from)
	out := make([]Observation, len(values))
	for i, v := range values {
		out[i] = Observation{
			Date: start.AddDate(0, 0, i*everyDays).Format(dayLayout),
			NDVI: v,
		}
	}
	return out
}

func TestEmergenceIsReadOffTheRise(t *testing.T) {
	// Bare soil, then a climb, then a peak. The crossing sits between the
	// second and third passes.
	series := curve("2026-06-10", 5, 0.16, 0.18, 0.34, 0.55, 0.72, 0.78)

	g, why, ok := DetectGreenUp(series)
	if !ok {
		t.Fatalf("no green-up detected: %s", why)
	}
	if g.Date < "2026-06-15" || g.Date > "2026-06-20" {
		t.Errorf("green-up at %s, expected between the second and third passes", g.Date)
	}
	if g.Amplitude < 0.6 {
		t.Errorf("amplitude = %v", g.Amplitude)
	}
	if g.Confidence != ConfidenceHigh {
		t.Errorf("confidence = %s for a clean five-day series", g.Confidence)
	}
}

func TestAFlatSeriesHasNoEmergenceToRead(t *testing.T) {
	// A fallow parcel, a failed sowing, or a boundary that is not on the
	// field it claims. All three must refuse rather than produce a date.
	series := curve("2026-06-10", 5, 0.17, 0.19, 0.18, 0.20, 0.19)

	if _, why, ok := DetectGreenUp(series); ok {
		t.Error("a flat series produced an emergence date")
	} else if why == "" {
		t.Error("refused with no reason")
	}
}

func TestTooFewObservationsRefuse(t *testing.T) {
	if _, _, ok := DetectGreenUp(curve("2026-06-10", 5, 0.2, 0.7)); ok {
		t.Error("two observations produced an emergence date")
	}
	if _, _, ok := DetectGreenUp(nil); ok {
		t.Error("an empty series produced an emergence date")
	}
}

func TestAMonsoonGapMakesTheDateLowConfidence(t *testing.T) {
	// The ordinary kharif case: one pass in June, nothing until August. The
	// arithmetic still yields a date; it is not evidence, and the confidence
	// has to say so.
	series := []Observation{
		{Date: "2026-06-12", NDVI: 0.16},
		{Date: "2026-08-05", NDVI: 0.68},
		{Date: "2026-08-15", NDVI: 0.74},
	}

	g, why, ok := DetectGreenUp(series)
	if !ok {
		t.Fatalf("refused: %s", why)
	}
	if g.Confidence != ConfidenceLow {
		t.Errorf("confidence = %s across a %d-day gap", g.Confidence, g.BracketGapDays)
	}
	if len(g.Caveats) == 0 {
		t.Error("no caveat for a date placed inside a two-month gap")
	}
}

func TestAMidSeasonDipIsNotReadAsEmergence(t *testing.T) {
	// A crop that greens, dips under cloud shoulder or stress, and recovers.
	// Searching back from the peak would place emergence at the recovery.
	series := curve("2026-06-10", 7, 0.15, 0.42, 0.66, 0.48, 0.71, 0.79)

	g, why, ok := DetectGreenUp(series)
	if !ok {
		t.Fatalf("refused: %s", why)
	}
	if g.Date > "2026-06-24" {
		t.Errorf("emergence placed at %s, which is the recovery rather than the "+
			"first rise", g.Date)
	}
}

func TestASeriesThatStartsAtItsPeakRefuses(t *testing.T) {
	// The window opened after the crop was already grown. There is no
	// emergence in the record to find.
	series := curve("2026-06-10", 5, 0.78, 0.72, 0.55, 0.34)
	if _, why, ok := DetectGreenUp(series); ok {
		t.Error("a declining series produced an emergence date")
	} else if why == "" {
		t.Error("refused with no reason")
	}
}

func TestUnusableReadingsAreDiscardedNotPropagated(t *testing.T) {
	series := []Observation{
		{Date: "not-a-date", NDVI: 0.9},
		{Date: "2026-06-10", NDVI: 0.16},
		{Date: "2026-06-15", NDVI: math.NaN()},
		{Date: "2026-06-20", NDVI: 0.40},
		{Date: "2026-06-25", NDVI: 0.70},
		{Date: "2026-06-30", NDVI: 0.76},
	}
	g, why, ok := DetectGreenUp(series)
	if !ok {
		t.Fatalf("refused: %s", why)
	}
	if g.Date == "" {
		t.Error("no date")
	}
}

func TestAnUnsortedSeriesGivesTheSameAnswer(t *testing.T) {
	// Observations arrive in whatever order the engine returns them.
	ordered := curve("2026-06-10", 5, 0.16, 0.18, 0.34, 0.55, 0.72, 0.78)
	shuffled := []Observation{ordered[3], ordered[0], ordered[5], ordered[1], ordered[4], ordered[2]}

	a, _, ok1 := DetectGreenUp(ordered)
	b, _, ok2 := DetectGreenUp(shuffled)
	if !ok1 || !ok2 {
		t.Fatal("one of the two orderings refused")
	}
	if a.Date != b.Date {
		t.Errorf("order changed the answer: %s vs %s", a.Date, b.Date)
	}
}

// ---------------------------------------------------------------------------
// The declared sowing date, checked
// ---------------------------------------------------------------------------

func TestADeclaredSowingDateInTheExpectedRangeAgrees(t *testing.T) {
	series := curve("2026-06-10", 5, 0.16, 0.18, 0.34, 0.55, 0.72, 0.78)
	// Emergence lands around 17 June; sowing ten days earlier is ordinary.
	c, ok := CheckSowingDate("2026-06-07", series)
	if !ok {
		t.Fatal("the check could not run")
	}
	if !c.Agrees {
		t.Errorf("a ten-day lag was reported as disagreement: %s", c.Note)
	}
}

func TestACropCannotEmergeBeforeItIsSown(t *testing.T) {
	// The most informative failure in the whole check. Either the date is
	// wrong or the boundary is on somebody else's field.
	series := curve("2026-06-10", 5, 0.16, 0.18, 0.34, 0.55, 0.72, 0.78)

	c, ok := CheckSowingDate("2026-07-20", series)
	if !ok {
		t.Fatal("the check could not run")
	}
	if c.Agrees {
		t.Error("a sowing date after emergence was accepted")
	}
	if c.DifferenceD >= 0 {
		t.Errorf("difference = %d, want negative", c.DifferenceD)
	}
	if c.Note == "" {
		t.Error("no explanation")
	}
}

func TestALongDelayIsReportedWithoutBeingCalledFraud(t *testing.T) {
	// Re-sowing after a failed germination looks exactly like a mis-entered
	// date. The note must not pick one.
	series := curve("2026-07-20", 5, 0.16, 0.18, 0.34, 0.55, 0.72, 0.78)

	c, ok := CheckSowingDate("2026-06-01", series)
	if !ok {
		t.Fatal("the check could not run")
	}
	if c.Agrees {
		t.Error("a fifty-day lag was reported as agreement")
	}
	for _, word := range []string{"fraud", "false", "lying"} {
		if contains(c.Note, word) {
			t.Errorf("the note accuses rather than reports: %s", c.Note)
		}
	}
	if !contains(c.Note, "re-sowing") {
		t.Errorf("the note does not offer the innocent explanation: %s", c.Note)
	}
}

func TestADisagreementOnALowConfidenceGreenUpIsQualified(t *testing.T) {
	// Arithmetic across a monsoon gap is not a finding, and the note has to
	// say so or an officer will read it as one.
	series := []Observation{
		{Date: "2026-06-12", NDVI: 0.16},
		{Date: "2026-08-05", NDVI: 0.68},
		{Date: "2026-08-15", NDVI: 0.74},
	}
	c, ok := CheckSowingDate("2026-05-01", series)
	if !ok {
		t.Fatal("the check could not run")
	}
	if c.Agrees {
		t.Fatal("expected a disagreement to qualify")
	}
	if !contains(c.Note, "low confidence") {
		t.Errorf("a low-confidence disagreement was stated flatly: %s", c.Note)
	}
}

func TestTheCheckRefusesWhenTheSeriesCannotSupportIt(t *testing.T) {
	flat := curve("2026-06-10", 5, 0.17, 0.19, 0.18, 0.20)
	if _, ok := CheckSowingDate("2026-06-01", flat); ok {
		t.Error("a flat series produced a sowing check")
	}
	good := curve("2026-06-10", 5, 0.16, 0.18, 0.34, 0.55, 0.72, 0.78)
	if _, ok := CheckSowingDate("not-a-date", good); ok {
		t.Error("an unparseable declared date produced a check")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && fmt.Sprintf("%s", haystack) != "" &&
		indexOf(haystack, needle) >= 0
}

func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
