// Package pointndvi reads the optical record at the spots the farmer filmed.
//
// # What this adds, and what it deliberately leaves alone
//
// The parcel-wide NDVI analysis is untouched. It keeps producing the same
// series, the same peak, the same RRVL, and it remains what the loss figure is
// derived from. This package runs the *same* scoring over a small patch around
// each point of the walk and reports what the two say about each other. It is
// an addition, and nothing here changes a number that existed before it.
//
// # Why the spots are worth their own reading
//
// A field mean answers "how did this parcel do". It cannot answer the question
// a fraud check asks, which is whether the ground the farmer filmed looks, from
// orbit, like what the footage shows. Two cases the mean cannot separate:
//
//   - Footage of a devastated crop, taken where the satellite shows vigorous
//     growth. Averaged into a field mean this disappears. Read at the spot it
//     is the strongest thing this platform can hold, because the usual
//     defence -- "the satellite averages over the whole field" -- is
//     unavailable: this *is* that spot.
//   - Four clips from the one ruined corner of a healthy field. The mean is
//     dragged down slightly and looks like a mediocre season. Read per point,
//     the pattern is visible: every point far worse than the field it sits in.
//
// # Why the margins here are wide
//
// A 20 m circle is about 1,250 m², or a dozen Sentinel-2 pixels, several of
// them mixed with whatever lies beyond the buffer, and the shape is clipped to
// the parcel so a point near a corner keeps a fraction of that. A reading over
// a dozen noisy pixels cannot support a fine distinction, so nothing here
// speaks until the difference is far outside what that noise can produce. The
// thresholds are judgements, they are named as such, and each is stated in the
// report beside the number it governed.
package pointndvi

import (
	"github.com/agribid/esurvey-platform/backend/internal/assessment"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// Thresholds. Every one of these is a judgement, not a measurement.
const (
	// MinAreaM2 is the least ground a reading may rest on and still be
	// compared with anything.
	//
	// Four hundred square metres is about four Sentinel-2 pixels once mixed
	// edges are allowed for. Below it the mean is an average of noise, and a
	// comparison drawn from it would be a confident statement about nothing.
	MinAreaM2 = 400.0

	// MinObservations is the fewest cloud-free passes a point needs.
	//
	// Two: one to establish what was growing and one to show what became of
	// it. A single observation has no loss in it -- only a value -- and
	// reporting a reduction from one pass would be arithmetic on an
	// assumption.
	MinObservations = 2

	// ContradictionMarginPoints is how far apart the footage and the satellite
	// must be, at the same spot, before the disagreement is reported.
	//
	// The footage says most of the crop at this point is gone while the
	// satellite says almost none of the vigour is. Fifty points of separation
	// is far outside anything a dozen mixed pixels can invent, and it is the
	// difference between "these two instruments are noisy" and "these two
	// instruments cannot both be describing this ground".
	ContradictionMarginPoints = 50.0

	// FootageSevereSCV is the vision score above which the footage is claiming
	// serious damage at that point.
	FootageSevereSCV = 50.0

	// SelectiveMarginPoints is how much worse than its own field the sampled
	// ground must be before the pattern is reported.
	//
	// Not an accusation. A farmer records from where they are standing and is
	// entitled to, and a real loss is often genuinely patchy. What it means is
	// that the footage is not representative of the parcel, which is something
	// an officer applying a percentage to the whole area has to know.
	SelectiveMarginPoints = 25.0
)

// Spot is one point's own optical record, as gathered.
type Spot struct {
	StationNo int

	// AreaM2 is how much of the parcel the sampled patch covered, after the
	// buffer was clipped to the boundary.
	AreaM2 float64

	// OutsideParcel is true where the farmer's recorded position itself fell
	// outside the boundary, and only part of the buffer reached back in.
	OutsideParcel bool

	// Series is the point's own cloud-free observations, from the same engine
	// and the same window as the parcel's.
	Series []satellite.NDVIPoint

	// FootageSCV is what the vision pipeline measured in the clip taken here,
	// where it measured anything. Nil otherwise, and the contradiction check
	// then has nothing to compare and says nothing.
	FootageSCV *float64
}

// Reading is one point, scored.
type Reading struct {
	StationNo int     `json:"station_no"`
	AreaM2    float64 `json:"area_m2"`

	Observations int     `json:"observations"`
	PeakNDVI     float64 `json:"peak_ndvi"`
	LatestNDVI   float64 `json:"latest_ndvi"`

	// RRVLPercent is the reduction in vegetation at this point, computed by
	// the same function that computes the parcel's. Identical arithmetic on
	// both sides is what makes the two comparable at all.
	RRVLPercent float64 `json:"rrvl_percent"`

	// AgainstFieldPoints is this point's RRVL less the parcel's. Positive
	// means this ground did worse than the field around it.
	AgainstFieldPoints float64 `json:"against_field_points"`

	// FootageSCV is what the clip from here measured, for the row that prints
	// them side by side.
	FootageSCV *float64 `json:"footage_scv,omitempty"`

	// Weak marks a reading that stands but cannot carry a comparison, and
	// WeakBecause says which of the three reasons applies. The reading is
	// still reported: a patch that could not be read is part of the record.
	//
	// A reason rather than a sentence. This report is printed in two
	// languages, and a package that returns English prose forces whoever
	// renders it either to print English in the Marathi half or to
	// reverse-engineer the wording. Neither is acceptable in a document that
	// decides a payment, so the wording lives where the other wording lives.
	Weak        bool     `json:"weak,omitempty"`
	WeakBecause Weakness `json:"weak_because,omitempty"`
}

// Weakness is why a reading cannot carry a comparison.
type Weakness string

const (
	// FewPasses: one cloud-free pass shows a value, not a change.
	FewPasses Weakness = "few_passes"
	// SmallPatch: too little parcel inside the buffer to read.
	SmallPatch Weakness = "small_patch"
	// RecordedOutside: the position itself was outside the boundary, so the
	// patch is of the nearest ground inside rather than of where they stood.
	RecordedOutside Weakness = "recorded_outside"
)

// Contradiction is one point where the footage and the satellite cannot both
// be describing the same ground.
type Contradiction struct {
	StationNo    int     `json:"station_no"`
	FootageSCV   float64 `json:"footage_scv"`
	PointRRVL    float64 `json:"point_rrvl_percent"`
	AreaM2       float64 `json:"area_m2"`
	Observations int     `json:"observations"`
}

// Comparison is everything this analysis concluded.
type Comparison struct {
	RadiusM float64 `json:"radius_m"`

	// FieldRRVLPercent is the parcel-wide figure, carried here unchanged so
	// the comparison can be checked without fetching it from elsewhere.
	FieldRRVLPercent float64 `json:"field_rrvl_percent"`

	Readings []Reading `json:"readings"`

	// CloudedPoints is the points no cloud-free pass covered, by number. An
	// absent point is a finding, not a gap: silence would let it read as
	// agreement with whatever the footage claimed there.
	CloudedPoints []int `json:"clouded_points,omitempty"`

	// Contradictions are the points where the footage claims serious damage
	// and the satellite, on that same ground, does not.
	Contradictions []Contradiction `json:"contradictions,omitempty"`

	// SampledWorseThanFieldPoints is how much worse the readable points were
	// than the parcel as a whole, on average. Nil where fewer than two points
	// were readable, because one patch is a patch and not a pattern.
	SampledWorseThanFieldPoints *float64 `json:"sampled_worse_than_field_points,omitempty"`

	// ComparablePoints is how many readings the mean above rests on.
	ComparablePoints int `json:"comparable_points,omitempty"`

	// Skewed is true where the sampled ground was materially worse than the
	// field it sits in. Not an accusation -- a farmer records from where they
	// stand, and a real loss is often patchy -- but an officer applying a
	// percentage to the whole area has to know the footage is not the field.
	Skewed bool `json:"skewed,omitempty"`
}

// Input is what Compare needs.
type Input struct {
	RadiusM float64

	// FieldRRVLPercent is the parcel-wide reduction, already computed by the
	// existing analysis. Passed in rather than recomputed: there must be
	// exactly one of these numbers in the system.
	FieldRRVLPercent float64

	Spots []Spot
}

// Compare scores each point and works out what the set of them says.
//
// Returns nil where nothing was sampled at all, so a caller can leave the
// section out rather than print an empty one.
func Compare(in Input) *Comparison {
	if len(in.Spots) == 0 {
		return nil
	}

	radius := in.RadiusM
	if radius <= 0 {
		radius = 20
	}
	c := &Comparison{RadiusM: radius, FieldRRVLPercent: in.FieldRRVLPercent}

	var comparable []Reading
	for _, sp := range in.Spots {
		// The same scoring as the parcel. An error here means no observations,
		// which is a clouded patch rather than a fault.
		score, err := assessment.ScoreSatellite(sp.Series)
		if err != nil {
			c.CloudedPoints = append(c.CloudedPoints, sp.StationNo)
			continue
		}

		r := Reading{
			StationNo:          sp.StationNo,
			AreaM2:             sp.AreaM2,
			Observations:       score.ObservationCount,
			PeakNDVI:           score.BaselineNDVI,
			LatestNDVI:         score.IncidentNDVI,
			RRVLPercent:        score.RRVLPercent,
			AgainstFieldPoints: score.RRVLPercent - in.FieldRRVLPercent,
			FootageSCV:         sp.FootageSCV,
		}

		switch {
		case score.ObservationCount < MinObservations:
			r.Weak, r.WeakBecause = true, FewPasses
		case sp.AreaM2 < MinAreaM2:
			r.Weak, r.WeakBecause = true, SmallPatch
		case sp.OutsideParcel:
			r.Weak, r.WeakBecause = true, RecordedOutside
		}

		c.Readings = append(c.Readings, r)
		if !r.Weak {
			comparable = append(comparable, r)
		}
	}

	if len(c.Readings) == 0 {
		// Every patch was clouded. Nothing to compare, but the absences are
		// themselves worth printing.
		if len(c.CloudedPoints) == 0 {
			return nil
		}
		return c
	}

	// The footage against the satellite, at the same spot.
	//
	// Only on readings strong enough to carry it: a contradiction drawn from
	// three pixels is not a finding, it is a rounding error with an
	// accusation attached.
	for _, r := range comparable {
		if r.FootageSCV == nil {
			continue
		}
		if *r.FootageSCV >= FootageSevereSCV &&
			*r.FootageSCV-r.RRVLPercent >= ContradictionMarginPoints {
			c.Contradictions = append(c.Contradictions, Contradiction{
				StationNo:    r.StationNo,
				FootageSCV:   *r.FootageSCV,
				PointRRVL:    r.RRVLPercent,
				AreaM2:       r.AreaM2,
				Observations: r.Observations,
			})
		}
	}

	// Whether the ground filmed was representative of the field.
	if len(comparable) >= 2 {
		var sum float64
		for _, r := range comparable {
			sum += r.AgainstFieldPoints
		}
		mean := sum / float64(len(comparable))
		c.SampledWorseThanFieldPoints = &mean
		c.ComparablePoints = len(comparable)
		c.Skewed = mean >= SelectiveMarginPoints
	}

	return c
}
