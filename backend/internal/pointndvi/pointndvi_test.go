package pointndvi

import (
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// Reading the optical record at the spots the farmer filmed.
//
// Every finding this package produces is an allegation about a named person's
// claim, so the tests are mostly about what it must *not* say: on a thin
// reading, on a clouded patch, on a single pass, and on ordinary patchiness.
// A false contradiction here costs a farmer their payment.

// series builds observations rising to a peak and then falling to an end
// value, which is the shape ScoreSatellite reads.
func series(peak, end float64) []satellite.NDVIPoint {
	return []satellite.NDVIPoint{
		{Date: "2026-07-01", NDVI: peak * 0.6},
		{Date: "2026-08-01", NDVI: peak},
		{Date: "2026-09-20", NDVI: end},
	}
}

func scv(v float64) *float64 { return &v }

// A healthy patch: peak 0.70 falling only to 0.66.
func healthy() []satellite.NDVIPoint { return series(0.70, 0.66) }

// A ruined patch: peak 0.70 collapsing to near bare soil.
func ruined() []satellite.NDVIPoint { return series(0.70, 0.16) }

func full() float64 { return 1250 }

func TestNothingSampledIsNoSection(t *testing.T) {
	if Compare(Input{}) != nil {
		t.Error("a comparison was produced for a walk with nothing sampled")
	}
}

func TestFootageClaimingRuinWhereTheGroundIsGreenIsReported(t *testing.T) {
	// The case this whole package exists for. The clip from point 2 measures
	// severe damage; the satellite, over the same 20 m, shows the vigour
	// almost untouched. The usual defence -- that the satellite averages the
	// whole field -- is not available, because this is that spot.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 10,
		Spots: []Spot{{
			StationNo: 2, AreaM2: full(), Series: healthy(), FootageSCV: scv(82),
		}},
	})
	if c == nil {
		t.Fatal("no comparison")
	}
	if len(c.Contradictions) != 1 {
		t.Fatalf("%d contradictions reported; the footage says 82 and the "+
			"ground says almost nothing", len(c.Contradictions))
	}
	got := c.Contradictions[0]
	if got.StationNo != 2 {
		t.Errorf("the contradiction is reported at point %d", got.StationNo)
	}
	if got.FootageSCV != 82 {
		t.Errorf("the footage figure was lost: %v", got.FootageSCV)
	}
}

func TestFootageAndGroundAgreeingIsNotReported(t *testing.T) {
	// Severe damage in the clip, and the satellite says the same. That is a
	// corroborated claim, which is the ordinary case and must be silent.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 70,
		Spots: []Spot{{
			StationNo: 1, AreaM2: full(), Series: ruined(), FootageSCV: scv(80),
		}},
	})
	if len(c.Contradictions) != 0 {
		t.Errorf("a corroborated claim was reported as a contradiction: %+v",
			c.Contradictions)
	}
}

func TestAThinPatchNeverCarriesAContradiction(t *testing.T) {
	// A point near a corner keeps a fraction of its buffer. Three or four
	// pixels cannot support an allegation, however far apart the numbers land.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 10,
		Spots: []Spot{{
			StationNo: 4, AreaM2: MinAreaM2 - 1, Series: healthy(),
			FootageSCV: scv(95),
		}},
	})
	if len(c.Contradictions) != 0 {
		t.Error("a contradiction was drawn from a patch too small to read")
	}
	if len(c.Readings) != 1 || !c.Readings[0].Weak {
		t.Fatal("the thin reading should still be printed, marked weak")
	}
	if c.Readings[0].WeakBecause != SmallPatch {
		t.Errorf("the reading gives the wrong reason: %q",
			c.Readings[0].WeakBecause)
	}
}

func TestASinglePassNeverCarriesAContradiction(t *testing.T) {
	// One observation is a value, not a change. A reduction computed from it
	// would be arithmetic on an assumption.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 10,
		Spots: []Spot{{
			StationNo: 3, AreaM2: full(),
			Series:     []satellite.NDVIPoint{{Date: "2026-09-20", NDVI: 0.66}},
			FootageSCV: scv(90),
		}},
	})
	if len(c.Contradictions) != 0 {
		t.Error("a contradiction was drawn from a single pass")
	}
	if len(c.Readings) != 1 || !c.Readings[0].Weak {
		t.Fatal("the single-pass reading should be printed, marked weak")
	}
	if c.Readings[0].WeakBecause != FewPasses {
		t.Errorf("the reading gives the wrong reason: %q",
			c.Readings[0].WeakBecause)
	}
}

func TestAPatchWithNoPassIsReportedAsUnsampledNotAsAgreement(t *testing.T) {
	// Cloud over one corner. Saying nothing would let the absence read as
	// corroboration of whatever the footage claimed there.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 40,
		Spots: []Spot{{
			StationNo: 2, AreaM2: full(), Series: nil, FootageSCV: scv(88),
		}},
	})
	if c == nil {
		t.Fatal("a clouded patch produced no comparison at all, so the " +
			"absence would never be printed")
	}
	if len(c.Readings) != 0 {
		t.Error("a patch with no observation produced a reading")
	}
	if len(c.CloudedPoints) != 1 || c.CloudedPoints[0] != 2 {
		t.Errorf("the unsampled point is not named: %v", c.CloudedPoints)
	}
	if len(c.Contradictions) != 0 {
		t.Error("a contradiction was drawn from a patch with no observation")
	}
}

func TestRecordingOutsideTheBoundaryWeakensTheReading(t *testing.T) {
	// The buffer reached back into the field, so there is ground to read --
	// but it is not the ground the farmer was standing on, and a finding must
	// not be pinned on it.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 10,
		Spots: []Spot{{
			StationNo: 1, AreaM2: full(), OutsideParcel: true,
			Series: healthy(), FootageSCV: scv(90),
		}},
	})
	if len(c.Contradictions) != 0 {
		t.Error("a contradiction was pinned on a recording made outside the field")
	}
	if !c.Readings[0].Weak || c.Readings[0].WeakBecause != RecordedOutside {
		t.Errorf("the reading does not say the recording was outside: %q",
			c.Readings[0].WeakBecause)
	}
}

func TestFilmingOnlyTheWorstGroundIsReportedWithoutAccusation(t *testing.T) {
	// Four clips from the ruined parts of a field that did middlingly. Not
	// fraud -- the farmer records where they stand -- but an officer applying
	// a percentage to the whole area has to know the footage is not the field.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 20,
		Spots: []Spot{
			{StationNo: 1, AreaM2: full(), Series: ruined()},
			{StationNo: 2, AreaM2: full(), Series: ruined()},
		},
	})
	if c.SampledWorseThanFieldPoints == nil {
		t.Fatal("no comparison against the field was made")
	}
	if *c.SampledWorseThanFieldPoints < SelectiveMarginPoints {
		t.Fatalf("the sampled ground measured %.0f points worse; the fixture "+
			"is meant to be well past %.0f",
			*c.SampledWorseThanFieldPoints, SelectiveMarginPoints)
	}
	if !c.Skewed {
		t.Fatal("the pattern was not reported")
	}
	if c.ComparablePoints != 2 {
		t.Errorf("the mean rests on %d points, not the 2 that were readable",
			c.ComparablePoints)
	}
	// Filming the worst ground is not the same allegation as the footage and
	// the satellite disagreeing, and must never be reported as one.
	if len(c.Contradictions) != 0 {
		t.Error("filming the worst ground was reported as a contradiction")
	}
}

func TestOrdinaryPatchinessSaysNothing(t *testing.T) {
	// One point a little worse than the field and one a little better. This is
	// what every real field looks like, and a system that remarked on it would
	// remark on every claim.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 40,
		Spots: []Spot{
			{StationNo: 1, AreaM2: full(), Series: series(0.70, 0.40)},
			{StationNo: 2, AreaM2: full(), Series: series(0.70, 0.46)},
		},
	})
	if c.Skewed {
		t.Error("ordinary patchiness was reported as skewed sampling")
	}
	if len(c.Contradictions) != 0 {
		t.Error("ordinary patchiness produced a contradiction")
	}
}

func TestOnePointIsNotAPattern(t *testing.T) {
	// A single readable point cannot establish that the sampling was skewed:
	// one patch worse than the field is a patch, not a pattern.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 10,
		Spots: []Spot{
			{StationNo: 1, AreaM2: full(), Series: ruined()},
		},
	})
	if c.SampledWorseThanFieldPoints != nil {
		t.Error("a mean against the field was computed from one point")
	}
	if c.Skewed {
		t.Error("one point was reported as a pattern")
	}
}

func TestTheFieldFigureIsCarriedThroughUnchanged(t *testing.T) {
	// This package adds a reading; it must never restate the parcel's own
	// figure as something else. An officer comparing the two sections has to
	// find the same number in both.
	c := Compare(Input{
		RadiusM:          20,
		FieldRRVLPercent: 37.5,
		Spots:            []Spot{{StationNo: 1, AreaM2: full(), Series: healthy()}},
	})
	if c.FieldRRVLPercent != 37.5 {
		t.Errorf("the parcel figure came through as %v", c.FieldRRVLPercent)
	}
	r := c.Readings[0]
	if got := r.RRVLPercent - 37.5; got != r.AgainstFieldPoints {
		t.Errorf("the difference from the field is %v, not %v",
			r.AgainstFieldPoints, got)
	}
}
