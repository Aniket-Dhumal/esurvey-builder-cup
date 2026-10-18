package groundcv

import (
	"errors"
	"math"
	"testing"
)

// The specification's arithmetic, checked against the specification.
//
// This package is the ground signal the fusion weights above every other, so
// an error here is an error in the number that decides what a farmer is paid.
// The tests are written against the source document rather than against the
// implementation: the weights, the band endpoints and the denominator are all
// quoted, so a later edit that "simplifies" one of them fails here.

const tol = 1e-9

func near(t *testing.T, got, want float64, what string) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

func TestTheWeightsAreTheSpecificationsAndSumToOne(t *testing.T) {
	// w1 = 0.45, w2 = 0.35, w3 = 0.20.
	near(t, WeightLodging, 0.45, "w1 lodging")
	near(t, WeightSubmerged, 0.35, "w2 submerged")
	near(t, WeightNecrotic, 0.20, "w3 necrotic")

	if s := WeightLodging + WeightSubmerged + WeightNecrotic; math.Abs(s-1) > tol {
		t.Errorf("the weights sum to %v; a set that does not sum to 1 silently "+
			"rescales every score", s)
	}
}

func TestLodgingLossFollowsTheSeverityTable(t *testing.T) {
	// Erect | theta <= 15 | 0%
	// Mild  | 15 < theta <= 45 | 15-30%
	// Severe| 45 < theta <= 75 | 35-65%
	// Flat  | theta > 75 | 70-100%
	cases := []struct {
		theta, want float64
		class       string
	}{
		{0, 0, LodgingErect},
		{15, 0, LodgingErect},

		{15.0001, 15, LodgingMild}, // enters the band at its floor
		{30, 22.5, LodgingMild},    // halfway across gives the band's midpoint
		{45, 30, LodgingMild},      // leaves it at its ceiling

		{45.0001, 35, LodgingSevere},
		{60, 50, LodgingSevere},
		{75, 65, LodgingSevere},

		{75.0001, 70, LodgingProstrate},
		{82.5, 85, LodgingProstrate},
		{90, 100, LodgingProstrate},
	}

	for _, c := range cases {
		got := LodgingLoss(c.theta)
		if math.Abs(got-c.want) > 0.01 {
			t.Errorf("LodgingLoss(%v) = %.4f, want %.4f", c.theta, got, c.want)
		}
		if cl := LodgingClassFor(c.theta); cl != c.class {
			t.Errorf("LodgingClassFor(%v) = %q, want %q", c.theta, cl, c.class)
		}
	}
}

func TestLodgingLossNeverGoesBackwards(t *testing.T) {
	// A crop lying flatter cannot be scored as less damaged. Monotonicity is
	// the property an officer will assume without being told, and the one an
	// edit to the bands is most likely to break.
	prev := -1.0
	for theta := 0.0; theta <= 90.0; theta += 0.25 {
		got := LodgingLoss(theta)
		if got < prev-tol {
			t.Fatalf("loss fell from %.3f to %.3f at theta=%.2f", prev, got, theta)
		}
		if got < 0 || got > 100 {
			t.Fatalf("LodgingLoss(%v) = %v, outside [0,100]", theta, got)
		}
		prev = got
	}
}

func TestTheBandGapsInTheSpecificationAreKept(t *testing.T) {
	// The specification leaves 30->35 and 65->70 unbridged. Smoothing them
	// away would be inventing a curve nobody wrote down, so the steps stay and
	// this test says so on purpose.
	if a, b := LodgingLoss(45), LodgingLoss(45.001); b-a < 4.9 {
		t.Errorf("the mild/severe step was smoothed away: %.3f -> %.3f", a, b)
	}
	if a, b := LodgingLoss(75), LodgingLoss(75.001); b-a < 4.9 {
		t.Errorf("the severe/prostrate step was smoothed away: %.3f -> %.3f", a, b)
	}
}

func TestTheDenominatorExcludesBackground(t *testing.T) {
	// alpha = |C2| / (|C1| + |C2| + |C3|). C4 is not in it. A frame with more
	// sky in it must not read as a less damaged field, and this is the whole
	// reason the specification writes the denominator out longhand.
	m := Measurements{
		StemAngleDegrees: 0,
		Keyframes:        12,
		Quality:          0.9,
		Classes: ClassPixels{
			HealthyFoliage: 5000,
			Submerged:      3000,
			Necrotic:       2000,
			Background:     0,
		},
	}
	tight, err := Evaluate(m)
	if err != nil {
		t.Fatal(err)
	}

	m.Classes.Background = 990_000 // the same field, filmed from further back
	wide, err := Evaluate(m)
	if err != nil {
		t.Fatal(err)
	}

	near(t, tight.SCV, wide.SCV, "S_CV with and without background")
	near(t, wide.SubmergedRatio, 0.3, "submerged ratio")
	near(t, wide.NecroticRatio, 0.2, "necrotic ratio")

	// The share is what shows the framing changed, since the score did not.
	if wide.CropPixelShare >= tight.CropPixelShare {
		t.Error("crop pixel share did not fall when the frame filled with background")
	}
}

func TestAWorkedScore(t *testing.T) {
	// Severe lodging at 60 degrees, a third of the crop submerged, a fifth
	// necrotic. Worked by hand from the formula:
	//
	//   LodgingLoss(60)        = 50
	//   alpha_submerged        = 3000/10000 = 0.30
	//   alpha_necrotic         = 2000/10000 = 0.20
	//   S_CV = 0.45*50 + 0.35*30 + 0.20*20
	//        = 22.5  + 10.5      + 4.0      = 37.0
	got, err := Evaluate(Measurements{
		StemAngleDegrees: 60,
		Keyframes:        18,
		Quality:          0.88,
		Classes: ClassPixels{
			HealthyFoliage: 5000, Submerged: 3000, Necrotic: 2000, Background: 4000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	near(t, got.SCV, 37.0, "S_CV")
	near(t, got.LodgingLoss, 50, "lodging component")
	if got.LodgingClass != LodgingSevere {
		t.Errorf("class = %q, want %q", got.LodgingClass, LodgingSevere)
	}
}

func TestTheScoreIsCappedAtOneHundred(t *testing.T) {
	// Flat, wholly submerged. 0.45*100 + 0.35*100 = 80, so the cap is not
	// reachable by the arithmetic as written — but min() is in the
	// specification and the cap must hold if a weight is ever revised upward.
	got, err := Evaluate(Measurements{
		StemAngleDegrees: 90,
		Keyframes:        20,
		Quality:          1,
		Classes:          ClassPixels{Submerged: 10000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.SCV > 100 {
		t.Errorf("S_CV = %v, above the cap", got.SCV)
	}
	near(t, got.SCV, 80, "flat and wholly submerged")
}

func TestAnUndamagedFieldScoresZero(t *testing.T) {
	got, err := Evaluate(Measurements{
		StemAngleDegrees: 3,
		Keyframes:        20,
		Quality:          0.95,
		Classes:          ClassPixels{HealthyFoliage: 40000, Background: 10000},
	})
	if err != nil {
		t.Fatal(err)
	}
	near(t, got.SCV, 0, "an erect, healthy field")
	if got.LodgingClass != LodgingErect {
		t.Errorf("class = %q, want %q", got.LodgingClass, LodgingErect)
	}
}

func TestQualityDoesNotChangeTheScore(t *testing.T) {
	// q_cv decides how much weight the score is given in the fusion. It is not
	// damage, and mixing it in here would double-count it: a blurry video of a
	// flattened field would score as less damaged rather than as less certain.
	base := Measurements{
		StemAngleDegrees: 55,
		Keyframes:        14,
		Classes:          ClassPixels{HealthyFoliage: 6000, Submerged: 2000, Necrotic: 2000},
	}

	base.Quality = 0.95
	good, err := Evaluate(base)
	if err != nil {
		t.Fatal(err)
	}
	base.Quality = 0.15
	poor, err := Evaluate(base)
	if err != nil {
		t.Fatal(err)
	}

	near(t, good.SCV, poor.SCV, "S_CV across quality")
	if good.Quality == poor.Quality {
		t.Error("quality was not carried through for the fusion to use")
	}
}

func TestMeasurementsThatCannotSupportAScoreAreRefused(t *testing.T) {
	ok := ClassPixels{HealthyFoliage: 5000, Submerged: 3000, Necrotic: 2000}

	cases := []struct {
		name string
		m    Measurements
		want error
	}{
		{
			"a lens pointed at the sky",
			Measurements{Keyframes: 10, Quality: 0.9, Classes: ClassPixels{Background: 900000}},
			ErrNoCropPixels,
		},
		{
			"a denominator too small to divide by honestly",
			Measurements{Keyframes: 10, Quality: 0.9,
				Classes: ClassPixels{HealthyFoliage: 40, Submerged: 20}},
			ErrNoCropPixels,
		},
		{"nothing was scored", Measurements{Keyframes: 0, Quality: 0.9, Classes: ok}, ErrNoKeyframes},
		{"an impossible angle", Measurements{Keyframes: 10, Quality: 0.9,
			StemAngleDegrees: 140, Classes: ok}, ErrAngleOutOfRange},
		{"a negative angle", Measurements{Keyframes: 10, Quality: 0.9,
			StemAngleDegrees: -1, Classes: ok}, ErrAngleOutOfRange},
		{"quality above one", Measurements{Keyframes: 10, Quality: 1.5, Classes: ok},
			ErrQualityOutOfRange},
		{"quality below zero", Measurements{Keyframes: 10, Quality: -0.1, Classes: ok},
			ErrQualityOutOfRange},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Evaluate(c.m)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if got.SCV != 0 {
				t.Errorf("a refusal carried a score out with it: %v", got.SCV)
			}
		})
	}
}

func TestNaNIsRefusedRatherThanPropagated(t *testing.T) {
	// A NaN angle would sail through every comparison and produce a NaN score,
	// which serialises as null and reads downstream as "no damage".
	ok := ClassPixels{HealthyFoliage: 5000, Submerged: 3000, Necrotic: 2000}

	if _, err := Evaluate(Measurements{
		StemAngleDegrees: math.NaN(), Keyframes: 10, Quality: 0.9, Classes: ok,
	}); !errors.Is(err, ErrAngleOutOfRange) {
		t.Error("a NaN stem angle was not refused")
	}
	if _, err := Evaluate(Measurements{
		StemAngleDegrees: 30, Keyframes: 10, Quality: math.NaN(), Classes: ok,
	}); !errors.Is(err, ErrQualityOutOfRange) {
		t.Error("a NaN quality was not refused")
	}
}

// --- the walk, not one corner of it -----------------------------------------
//
// Four points across a field can disagree, and that disagreement is the whole
// reason a farmer is sent to four points (docs/EVIDENCE_REVALIDATION.md, V1).

func TestTheWalkIsWeightedByTheCropEachPointActuallyShowed(t *testing.T) {
	// One point saw a field's worth of crop and no damage; another glimpsed a
	// few pixels of flattened crop past a bund. The field is not half
	// flattened, and an unweighted average would say it was.
	wide := Measurements{
		StemAngleDegrees: 5,
		Classes:          ClassPixels{HealthyFoliage: 90_000, Background: 10_000},
		Quality:          0.9, Keyframes: 20,
	}
	glimpse := Measurements{
		StemAngleDegrees: 80,
		Classes:          ClassPixels{Necrotic: 1_000, Background: 99_000},
		Quality:          0.2, Keyframes: 4,
	}

	combined, err := Combine([]Measurements{wide, glimpse})
	if err != nil {
		t.Fatal(err)
	}
	if combined.Keyframes != 24 {
		t.Errorf("keyframes = %d, want every frame of the walk", combined.Keyframes)
	}
	if combined.Classes.HealthyFoliage != 90_000 || combined.Classes.Necrotic != 1_000 {
		t.Errorf("pixels did not add up: %+v", combined.Classes)
	}
	// 91,000 crop pixels, 90,000 of them at 5 degrees.
	if combined.StemAngleDegrees > 10 {
		t.Errorf("stem angle = %.1f; a glimpse dragged the field's lodging with it",
			combined.StemAngleDegrees)
	}
	if combined.Quality > 0.9 || combined.Quality < 0.85 {
		t.Errorf("quality = %.2f; want it weighted toward the point that saw the crop",
			combined.Quality)
	}
}

func TestAWalkThatSawNoCropIsRefusedRatherThanScored(t *testing.T) {
	sky := Measurements{Classes: ClassPixels{Background: 50_000}, Keyframes: 8}
	if _, err := Combine([]Measurements{sky, sky}); !errors.Is(err, ErrNoCropPixels) {
		t.Fatalf("err = %v, want ErrNoCropPixels", err)
	}
	if _, err := Combine(nil); !errors.Is(err, ErrNoCropPixels) {
		t.Fatalf("err = %v for no clips at all, want ErrNoCropPixels", err)
	}
}

func TestAHeightRatioSurvivesOnlyWhereItWasMeasured(t *testing.T) {
	ratio := 0.4
	with := Measurements{
		Classes:           ClassPixels{HealthyFoliage: 10_000},
		CanopyHeightRatio: &ratio,
	}
	without := Measurements{Classes: ClassPixels{HealthyFoliage: 10_000}}

	combined, err := Combine([]Measurements{with, without})
	if err != nil {
		t.Fatal(err)
	}
	if combined.CanopyHeightRatio == nil {
		t.Fatal("the one measured height was dropped")
	}
	if *combined.CanopyHeightRatio != ratio {
		t.Errorf("height ratio = %.2f; a point that measured none must not dilute it",
			*combined.CanopyHeightRatio)
	}
}
