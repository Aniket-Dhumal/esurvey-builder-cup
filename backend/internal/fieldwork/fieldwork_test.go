package fieldwork

import (
	"strings"
	"testing"
)

// The whole point of this package is that an officer reads a derivation
// rather than a number, so the tests are as much about what is said as about
// what is computed.

func i(v int) *int { return &v }

func quadrat(expected, present, lodged int) *Measurement {
	return &Measurement{
		QuadratSideM:   1,
		PlantsExpected: i(expected),
		PlantsPresent:  i(present),
		PlantsLodged:   i(lodged),
		CaptureID:      "cap-1",
	}
}

func TestStandLossIsCountedNotJudged(t *testing.T) {
	d := Derive(quadrat(40, 26, 0))

	if d.StandLossPercent == nil {
		t.Fatal("no stand loss from a complete count")
	}
	if *d.StandLossPercent != 35 {
		t.Errorf("stand loss = %v, want 35", *d.StandLossPercent)
	}
	if len(d.Working) == 0 {
		t.Fatal("no arithmetic was shown")
	}
	// An officer must be able to check the sum without trusting it.
	if !strings.Contains(d.Working[0], "40") || !strings.Contains(d.Working[0], "26") {
		t.Errorf("the working does not show its inputs: %s", d.Working[0])
	}
}

func TestLodgingIsOfWhatIsStandingNotOfWhatWasExpected(t *testing.T) {
	// A lodged plant is not a missing plant. Measuring lodging against the
	// expected stand would double-count the loss.
	d := Derive(quadrat(40, 20, 10))

	if d.LodgedPercent == nil {
		t.Fatal("no lodging figure")
	}
	if *d.LodgedPercent != 50 {
		t.Errorf("lodged = %v; 10 of 20 standing is 50%%, not %v of the expected 40",
			*d.LodgedPercent, 100*10/40)
	}
}

func TestMorePlantsThanExpectedIsExplainedRatherThanNegative(t *testing.T) {
	// Spacing varies, and a farmer who sowed thicker than the recommendation
	// has not caused a negative loss.
	d := Derive(quadrat(30, 42, 0))

	if d.StandLossPercent == nil {
		t.Fatal("no figure at all")
	}
	if *d.StandLossPercent != 0 {
		t.Errorf("stand loss = %v, want 0 rather than negative", *d.StandLossPercent)
	}
	var explained bool
	for _, c := range d.Caveats {
		if strings.Contains(c, "different spacing") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("nothing explains the over-count: %v", d.Caveats)
	}
}

func TestASmallSampleSaysHowMuchOneItemMovesIt(t *testing.T) {
	// Three damaged of five is 60%, and one more would make it 80%. An
	// officer who is not told that will read it as a measurement.
	m := &Measurement{
		QuadratSideM: 1,
		OrganSampled: i(5), OrganDamaged: i(3),
		CaptureID: "cap-1",
	}
	d := Derive(m)

	if d.OrganDamagePercent == nil || *d.OrganDamagePercent != 60 {
		t.Fatalf("organ damage = %v", d.OrganDamagePercent)
	}
	var warned bool
	for _, c := range d.Caveats {
		if strings.Contains(c, "single item") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("a five-item sample carried no warning: %v", d.Caveats)
	}
}

func TestALargeSampleNeedsNoWarning(t *testing.T) {
	m := &Measurement{
		QuadratSideM: 1,
		OrganSampled: i(100), OrganDamaged: i(60),
		CaptureID: "cap-1",
	}
	for _, c := range Derive(m).Caveats {
		if strings.Contains(c, "single item") {
			t.Errorf("a hundred-item sample was warned about: %s", c)
		}
	}
}

func TestAQuadratWithNoPhotographIsTestimony(t *testing.T) {
	// The difference between a count a second officer can check and one they
	// have to take on trust.
	m := quadrat(40, 26, 0)
	m.CaptureID = ""

	var said bool
	for _, c := range Derive(m).Caveats {
		if strings.Contains(c, "testimony") {
			said = true
		}
	}
	if !said {
		t.Error("an unphotographed quadrat was not distinguished from one with a frame")
	}
}

func TestASmallFrameIsFlaggedInBothDirections(t *testing.T) {
	// A half-metre frame placed where damage is worst reads high; placed
	// where it is lightest it reads low. Saying only the first would make the
	// caveat an accusation.
	m := quadrat(10, 4, 0)
	m.QuadratSideM = 0.5

	var found string
	for _, c := range Derive(m).Caveats {
		if strings.Contains(c, "square metre") {
			found = c
		}
	}
	if found == "" {
		t.Fatal("a sub-metre frame was not remarked on")
	}
	if !strings.Contains(found, "high") || !strings.Contains(found, "low") {
		t.Errorf("the caveat runs only one way: %s", found)
	}
}

func TestPlantsPerSquareMetreLetsTwoFramesBeCompared(t *testing.T) {
	// One officer uses a square metre, another a two-metre frame. Without a
	// density the two counts are not comparable at all.
	small := &Measurement{QuadratSideM: 1, PlantsPresent: i(30), CaptureID: "c"}
	large := &Measurement{QuadratSideM: 2, PlantsPresent: i(120), CaptureID: "c"}

	ds, dl := Derive(small), Derive(large)
	if ds.PlantsPerM2 == nil || dl.PlantsPerM2 == nil {
		t.Fatal("no density")
	}
	if *ds.PlantsPerM2 != 30 || *dl.PlantsPerM2 != 30 {
		t.Errorf("densities %v and %v; the same stand measured over two frames "+
			"should match", *ds.PlantsPerM2, *dl.PlantsPerM2)
	}
}

func TestNothingIsDerivedFromNothing(t *testing.T) {
	d := Derive(&Measurement{QuadratSideM: 1, CaptureID: "c"})
	if d.StandLossPercent != nil || d.LodgedPercent != nil || d.OrganDamagePercent != nil {
		t.Errorf("figures were produced from no counts: %+v", d)
	}
	if Derive(nil).StandLossPercent != nil {
		t.Error("a nil measurement produced a figure")
	}
}

// ---------------------------------------------------------------------------
// Several quadrats
// ---------------------------------------------------------------------------

func TestQuadratsThatDisagreeSayTheFieldIsUneven(t *testing.T) {
	// Not that somebody miscounted. Uneven damage is the ordinary case -- a
	// hail swathe crosses part of a field -- and the disagreement is itself
	// the finding.
	s := Summarise([]*Measurement{
		quadrat(40, 38, 0), // 5%
		quadrat(40, 12, 0), // 70%
		quadrat(40, 22, 0), // 45%
	}, 2.0)

	if s.SpreadStandLoss == nil {
		t.Fatal("no spread reported")
	}
	if *s.SpreadStandLoss < 60 {
		t.Errorf("spread = %v, want the full range", *s.SpreadStandLoss)
	}
	var said bool
	for _, c := range s.Caveats {
		if strings.Contains(c, "uneven") {
			said = true
			if strings.Contains(c, "wrong") && !strings.Contains(c, "than that any") {
				t.Errorf("the caveat reads as an accusation of miscounting: %s", c)
			}
		}
	}
	if !said {
		t.Errorf("disagreeing quadrats produced no note: %v", s.Caveats)
	}
}

func TestOneQuadratIsASpotCheckAndSaysSo(t *testing.T) {
	s := Summarise([]*Measurement{quadrat(40, 20, 0)}, 2.0)

	if s.SupportsAFigure {
		t.Error("a single quadrat was reported as supporting a figure")
	}
	var said bool
	for _, c := range s.Caveats {
		if strings.Contains(c, "one square metre") {
			said = true
		}
	}
	if !said {
		t.Errorf("a single quadrat was not qualified: %v", s.Caveats)
	}
}

func TestThreeQuadratsSupportAFigure(t *testing.T) {
	s := Summarise([]*Measurement{
		quadrat(40, 20, 0), quadrat(40, 22, 0), quadrat(40, 18, 0),
	}, 2.0)

	if !s.SupportsAFigure {
		t.Error("three photographed quadrats did not support a figure")
	}
	if s.MeanStandLoss == nil {
		t.Fatal("no mean")
	}
	if *s.MeanStandLoss < 45 || *s.MeanStandLoss > 55 {
		t.Errorf("mean stand loss = %v, want about 50", *s.MeanStandLoss)
	}
}

func TestTheSamplingFractionIsStatedPlainly(t *testing.T) {
	// Three square metres of a two-hectare field is 0.015 per cent of it.
	// Everything above is a sample, and an officer signing a figure for the
	// whole field should see how little of it was looked at.
	s := Summarise([]*Measurement{
		quadrat(40, 20, 0), quadrat(40, 22, 0), quadrat(40, 18, 0),
	}, 2.0)

	if s.SamplingFraction == "" {
		t.Fatal("no sampling fraction")
	}
	if !strings.Contains(s.SamplingFraction, "2.00 ha") {
		t.Errorf("the field size is missing: %s", s.SamplingFraction)
	}
	if s.TotalAreaM2 != 3 {
		t.Errorf("area sampled = %v m², want 3", s.TotalAreaM2)
	}
}

func TestNoMeasurementsSaysTheFigureRestsOnJudgement(t *testing.T) {
	// The honest description of what the system did before this package.
	s := Summarise(nil, 2.0)

	if s.SupportsAFigure {
		t.Error("no measurements were reported as supporting a figure")
	}
	if len(s.Caveats) == 0 || !strings.Contains(s.Caveats[0], "visual judgement") {
		t.Errorf("caveats = %v", s.Caveats)
	}
}

func TestUnphotographedQuadratsAreCounted(t *testing.T) {
	a, b := quadrat(40, 20, 0), quadrat(40, 22, 0)
	b.CaptureID = ""
	c := quadrat(40, 18, 0)
	c.CaptureID = ""

	s := Summarise([]*Measurement{a, b, c}, 2.0)
	if s.WithPhotographs != 1 {
		t.Errorf("with photographs = %d, want 1", s.WithPhotographs)
	}
	var said bool
	for _, cv := range s.Caveats {
		if strings.Contains(cv, "no photograph") {
			said = true
		}
	}
	if !said {
		t.Errorf("unphotographed quadrats were not noted: %v", s.Caveats)
	}
}

func TestThreeStandsThatAgreeHaveNoUnevennessNote(t *testing.T) {
	s := Summarise([]*Measurement{
		quadrat(40, 20, 0), quadrat(40, 21, 0), quadrat(40, 19, 0),
	}, 2.0)

	for _, c := range s.Caveats {
		if strings.Contains(c, "uneven") {
			t.Errorf("agreeing quadrats were called uneven: %s", c)
		}
	}
}

func TestAFieldWithNoAreaGivesNoFraction(t *testing.T) {
	// Better silent than dividing by zero on a form.
	s := Summarise([]*Measurement{quadrat(40, 20, 0)}, 0)
	if s.SamplingFraction != "" {
		t.Errorf("fraction = %q for a field of unknown size", s.SamplingFraction)
	}
}

// ---------------------------------------------------------------------------
// The suggestion the form offers
// ---------------------------------------------------------------------------

func TestTheExpectedStandFollowsFromTheSpacing(t *testing.T) {
	// Soybean at 45 cm between rows and 5 cm between plants, over a square
	// metre: about two rows of twenty.
	got, err := ExpectedStand(45, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got < 40 || got > 50 {
		t.Errorf("expected stand = %d, want about 44", got)
	}
}

func TestAFrameTwiceAsWideExpectsFourTimesAsMany(t *testing.T) {
	one, err := ExpectedStand(45, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ExpectedStand(45, 5, 2)
	if err != nil {
		t.Fatal(err)
	}
	if two < one*3 || two > one*5 {
		t.Errorf("%d over one metre and %d over two; area goes as the square",
			one, two)
	}
}

func TestNonsenseSpacingIsRefused(t *testing.T) {
	for _, c := range [][3]float64{{0, 5, 1}, {45, 0, 1}, {45, 5, 0}, {-45, 5, 1}} {
		if _, err := ExpectedStand(c[0], c[1], c[2]); err == nil {
			t.Errorf("spacing %v was accepted", c)
		}
	}
}

func TestSoilTypeIsAClosedListButMayBeUnrecorded(t *testing.T) {
	// Free text here becomes twelve spellings of "black cotton soil" and
	// nothing aggregates. Not recording it is allowed; recording a wrong one
	// is not.
	if !ValidSoilType("") {
		t.Error("an unrecorded soil type was refused")
	}
	if !ValidSoilType("black") || !ValidSoilType("  BLACK  ") {
		t.Error("a listed soil type was refused")
	}
	if ValidSoilType("kali mati") {
		t.Error("free text was accepted as a soil type")
	}
	if len(SoilTypes) == 0 {
		t.Fatal("no soil types offered")
	}
	for _, s := range SoilTypes {
		if s.Key == "" || s.Mr == "" || s.En == "" {
			t.Errorf("soil type %+v is missing a name; the officer choosing is "+
				"standing in a field and reads Marathi", s)
		}
	}
}
