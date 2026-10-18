// Package groundcv scores the damage visible in a farmer's survey video.
//
// # Why this is the signal that matters most
//
// The platform already refuses to publish a damage figure without ground
// imagery, and says why in assessment.Verify: Sentinel-2 at 10 m over a
// two-hectare smallholding establishes that a crop existed and roughly when it
// grew, and cannot measure how badly it was hurt. Radar adds what optical
// cannot see through cloud, but backscatter is a surface property, not a yield
// loss. Only the ground evidence sees the thing being claimed.
//
// The specification agrees and puts numbers on it: in its fusion the ground
// score carries 0.50 of the weight where the video is good and the sky is
// clear, and 0.65 where cloud has taken the optical signal away. No other
// input comes close.
//
// # What was actually missing
//
// Not the video, which the app has collected for a while. What was missing was
// any measurement of it. The panel looks at keyframes and reasons in prose; no
// part of the platform turned a stem angle or a submerged panicle into a
// number that could be checked, disagreed with, or compared against the
// agent's answer. That is what this package is.
//
// # What this package is not
//
// It is not a vision model. It takes Measurements — a lodging angle, semantic
// class pixel counts, a quality score — and applies the specification's
// arithmetic to them. Producing those measurements from video is the vision
// pipeline's job, and the split is deliberate: the same split the platform
// already uses for optical and radar, where a service observes and Go decides
// what the observation means. A model can be replaced without touching any of
// the reasoning here, and the reasoning can be argued with without retraining
// anything.
//
// Until a vision pipeline exists there are no Measurements, and an assessment
// reports the ground score absent — the same honest outcome radar produced for
// every deployment before satellite-intelligence was built. Nothing here
// invents a number in the meantime.
package groundcv

import (
	"errors"
	"math"
)

// Semantic classes, from the specification's segmentation of each keyframe.
//
// Mutually exclusive and exhaustive over the frame. C4 is everything that is
// not the crop, and it is deliberately absent from the loss ratios below: a
// frame that happens to include more sky or bund must not read as less damage.
type ClassPixels struct {
	// HealthyFoliage is C1: functional foliage and intact grain panicles.
	HealthyFoliage uint64 `json:"healthy_foliage"`
	// Submerged is C2: submerged panicles and silt-coated foliage.
	Submerged uint64 `json:"submerged"`
	// Necrotic is C3: foliar necrosis and post-flood blight lesions.
	Necrotic uint64 `json:"necrotic"`
	// Background is C4: ground, soil, water surface, sky. Not crop, and not
	// part of the denominator.
	Background uint64 `json:"background"`
}

// crop is the denominator for both loss ratios: C1 + C2 + C3.
func (c ClassPixels) crop() uint64 {
	return c.HealthyFoliage + c.Submerged + c.Necrotic
}

// Measurements is what a vision pipeline must produce for one parcel.
//
// This is the contract. Everything in this package is arithmetic over these
// fields, so a pipeline that fills them honestly gets a correct score and a
// pipeline that cannot fill them says so by not being run at all.
type Measurements struct {
	// StemAngleDegrees is the specification's theta_stem: the angle between
	// the dominant stem orientation and the gravimetric vertical, from a PCA
	// over the reconstructed stem point cloud. Zero is perfectly erect.
	StemAngleDegrees float64 `json:"stem_angle_degrees"`

	// Classes are the summed semantic pixel counts across the scored
	// keyframes.
	Classes ClassPixels `json:"classes"`

	// Quality is the specification's q_cv, in [0,1]: how much the vision
	// pipeline trusts its own output on this footage. Motion blur, darkness,
	// a lens pointed at the sky and too few usable keyframes all drive it
	// down. It carries no damage information and is never mixed into the
	// score — it decides how much weight the score is given later, which is a
	// different question and is answered in the fusion.
	Quality float64 `json:"quality"`

	// Keyframes is how many frames were scored. Reported so an officer can
	// see whether a score rests on a walk or on a glance.
	Keyframes int `json:"keyframes"`

	// CanopyHeightRatio is H_measured / H_expected where the pipeline could
	// reconstruct canopy height. The specification pairs it with the stem
	// angle in the lodging table as corroboration. Optional: height needs a
	// scaled reconstruction, which not every clip supports.
	CanopyHeightRatio *float64 `json:"canopy_height_ratio,omitempty"`
}

// Lodging classes, from the specification's severity table.
const (
	LodgingErect     = "erect"
	LodgingMild      = "mild"
	LodgingSevere    = "severe"
	LodgingProstrate = "prostrate"
)

// The specification's empirical weights for the three ground signals.
//
// They reflect standard agronomical yield-loss sensitivities under PMFBY
// operational guidelines, and they sum to 1. Lodging dominates because a
// flattened cereal cannot be harvested by machine at all, while necrosis is
// the mildest of the three because a leaf can die without the grain being lost.
const (
	WeightLodging   = 0.45
	WeightSubmerged = 0.35
	WeightNecrotic  = 0.20
)

// Score is the ground computer vision result for a parcel.
type Score struct {
	// SCV is the specification's S_CV, in [0,100].
	SCV float64 `json:"s_cv"`

	// The three components, kept separately because they are three different
	// physical claims about the field and an officer disputing the total
	// needs to see which one carries it.
	LodgingLoss     float64 `json:"lodging_loss"`
	SubmergedRatio  float64 `json:"submerged_ratio"`
	NecroticRatio   float64 `json:"necrotic_ratio"`
	LodgingClass    string  `json:"lodging_class"`
	StemAngleDegree float64 `json:"stem_angle_degrees"`

	Quality   float64 `json:"quality"`
	Keyframes int     `json:"keyframes"`

	// CropPixelShare is how much of the scored area was crop at all. A clip
	// that is mostly sky produces ratios over a tiny denominator, and this is
	// what shows that before the number is believed.
	CropPixelShare float64 `json:"crop_pixel_share"`

	// Stations is what each directed point showed, where the walk was
	// directed. The whole reason a farmer is sent to four points rather than
	// filming the corner that upset them is that four points can disagree --
	// and a figure resting on a field that is 70% damaged at one end and
	// untouched at the other is a different claim from one resting on a
	// field that is uniformly damaged.
	Stations []StationScore `json:"stations,omitempty"`

	// StationsMeasured is how many points the total rests on. One is a
	// glance; four is a survey, and an officer should be able to tell them
	// apart without reading the list.
	StationsMeasured int `json:"stations_measured,omitempty"`

	// Spread is the difference between the highest and lowest station score,
	// in points. Zero where only one station was measured, and meaningless
	// then -- StationsMeasured is what says so.
	Spread float64 `json:"spread,omitempty"`
}

// StationScore is one directed point's own reading.
type StationScore struct {
	// StationNo is 1 to 4 along the walk. Zero where the clip carried no
	// station -- an older report, or the photograph path.
	StationNo int `json:"station_no,omitempty"`

	SCV            float64 `json:"s_cv"`
	LodgingClass   string  `json:"lodging_class"`
	Quality        float64 `json:"quality"`
	Keyframes      int     `json:"keyframes"`
	CropPixelShare float64 `json:"crop_pixel_share"`
}

// Combine folds every station's measurements into one reading of the field.
//
// # Why the parts add up the way they do
//
// The pixel classes are counts over scored keyframes, so summing them across
// the walk is the same arithmetic the specification already does across frames
// within one clip -- four points of a field weigh in proportion to how much
// crop each actually showed, which is what an area-weighted loss means.
//
// The stem angle and the quality are not counts, so they are averaged weighted
// by the crop pixels behind them: a station that saw a handful of crop pixels
// through a lens pointed at the bund must not drag the field's lodging angle
// to its own.
//
// Returns ErrNoCropPixels when nothing in the walk showed crop, which is a
// finding about the footage rather than a fault.
func Combine(parts []Measurements) (Measurements, error) {
	if len(parts) == 0 {
		return Measurements{}, ErrNoCropPixels
	}

	var out Measurements
	var angleWeighted, qualityWeighted, heightWeighted, heightWeight, weight float64
	for _, m := range parts {
		crop := float64(m.Classes.HealthyFoliage + m.Classes.Submerged + m.Classes.Necrotic)
		out.Classes.HealthyFoliage += m.Classes.HealthyFoliage
		out.Classes.Submerged += m.Classes.Submerged
		out.Classes.Necrotic += m.Classes.Necrotic
		out.Classes.Background += m.Classes.Background
		out.Keyframes += m.Keyframes

		if crop <= 0 {
			continue
		}
		weight += crop
		angleWeighted += m.StemAngleDegrees * crop
		qualityWeighted += m.Quality * crop
		if m.CanopyHeightRatio != nil {
			heightWeighted += *m.CanopyHeightRatio * crop
			heightWeight += crop
		}
	}

	if weight <= 0 {
		return Measurements{}, ErrNoCropPixels
	}
	out.StemAngleDegrees = angleWeighted / weight
	out.Quality = qualityWeighted / weight
	if heightWeight > 0 {
		ratio := heightWeighted / heightWeight
		out.CanopyHeightRatio = &ratio
	}
	return out, nil
}

// Errors returned when the measurements cannot support a score.
//
// Refusing is a real outcome, not a failure: a score computed over four
// crop pixels is arithmetic, not evidence.
var (
	ErrNoCropPixels = errors.New(
		"the footage contains no crop pixels, so there is nothing to score")
	ErrAngleOutOfRange = errors.New(
		"the stem angle is not between 0 and 90 degrees")
	ErrQualityOutOfRange = errors.New(
		"the quality score is not between 0 and 1")
	ErrNoKeyframes = errors.New(
		"no keyframes were scored")
)

// minimumCropPixels is the denominator below which the ratios are noise.
//
// A hundred pixels of crop across every scored keyframe means the camera was
// not pointed at the field. The ratios would still divide, and would still
// produce a number between 0 and 100, which is exactly the problem.
const minimumCropPixels = 100

// Evaluate applies the specification's ground scoring to one parcel's
// measurements.
//
// S_CV = min(100, w1*LodgingLoss(theta) + w2*(alpha_submerged*100)
//
//   - w3*(alpha_necrotic*100))
func Evaluate(m Measurements) (Score, error) {
	if m.Keyframes <= 0 {
		return Score{}, ErrNoKeyframes
	}
	if m.StemAngleDegrees < 0 || m.StemAngleDegrees > 90 || math.IsNaN(m.StemAngleDegrees) {
		return Score{}, ErrAngleOutOfRange
	}
	if m.Quality < 0 || m.Quality > 1 || math.IsNaN(m.Quality) {
		return Score{}, ErrQualityOutOfRange
	}

	crop := m.Classes.crop()
	if crop < minimumCropPixels {
		return Score{}, ErrNoCropPixels
	}

	denom := float64(crop)
	alphaSubmerged := float64(m.Classes.Submerged) / denom
	alphaNecrotic := float64(m.Classes.Necrotic) / denom

	lodging := LodgingLoss(m.StemAngleDegrees)

	scv := WeightLodging*lodging +
		WeightSubmerged*(alphaSubmerged*100) +
		WeightNecrotic*(alphaNecrotic*100)

	total := denom + float64(m.Classes.Background)

	return Score{
		SCV:             math.Min(100, scv),
		LodgingLoss:     lodging,
		SubmergedRatio:  alphaSubmerged,
		NecroticRatio:   alphaNecrotic,
		LodgingClass:    LodgingClassFor(m.StemAngleDegrees),
		StemAngleDegree: m.StemAngleDegrees,
		Quality:         m.Quality,
		Keyframes:       m.Keyframes,
		CropPixelShare:  denom / total,
	}, nil
}

// LodgingClassFor names the band a stem angle falls in.
func LodgingClassFor(theta float64) string {
	switch {
	case theta <= 15:
		return LodgingErect
	case theta <= 45:
		return LodgingMild
	case theta <= 75:
		return LodgingSevere
	default:
		return LodgingProstrate
	}
}

// LodgingLoss converts a stem angle into a percentage yield loss.
//
// # Why this interpolates
//
// The specification gives bands rather than a function: erect is 0% loss, mild
// lodging 15-30%, severe 35-65%, prostrate 70-100%. Taking a band's midpoint
// would make the loss a step function, so a stem at 44 degrees and one at 46
// would differ by seventeen points of yield on a boundary that is itself an
// approximation. Two farmers on either side of it would be paid very
// differently for fields that look the same.
//
// So the loss moves linearly across each band, between the endpoints the
// specification gives. The jumps the specification does leave between bands —
// 30 to 35, and 65 to 70 — are kept, because they are in the source and
// smoothing them away would be inventing a curve nobody wrote down.
func LodgingLoss(theta float64) float64 {
	switch {
	case theta <= 15:
		return 0
	case theta <= 45:
		return lerp(theta, 15, 45, 15, 30)
	case theta <= 75:
		return lerp(theta, 45, 75, 35, 65)
	default:
		// Flat by 90 degrees; the crop cannot lie further down than the ground.
		return lerp(math.Min(theta, 90), 75, 90, 70, 100)
	}
}

func lerp(x, x0, x1, y0, y1 float64) float64 {
	if x1 == x0 {
		return y0
	}
	return y0 + (x-x0)/(x1-x0)*(y1-y0)
}
