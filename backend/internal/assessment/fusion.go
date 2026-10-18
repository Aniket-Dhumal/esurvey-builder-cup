package assessment

import (
	"math"

	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
)

// The specification's multi-modal fusion, as a cross-check rather than as the
// published figure.
//
// # The decision this file makes, and why
//
// The specification reconciles the ground score, the satellite score and the
// terrain score into a Composite Damage Percentage and treats that as the
// answer. This package's own doc comment records that an earlier version did
// exactly that, and that it was deleted: "two competing damage models in one
// service is a defect waiting to be shipped". The panel owns the published
// number, and Verify re-checks it.
//
// Both positions are right about different things, and the disagreement is
// resolvable. What the specification is really describing is not a second
// opinion to be averaged with the first — it is a way of noticing when the
// instruments disagree with the assessment. Its own machinery says so: the
// inter-modal variance penalty and the RED_ANOMALY_ESCALATE tier exist to
// catch divergence, not to price a claim.
//
// So the arithmetic here is the specification's, exactly, and the result is
// never published as damage. It escalates. A composite that lands far from the
// panel's figure means one of them is wrong about a field somebody is being
// paid for, and that is a case for a human, which is precisely what the
// specification's red tier asks for.
//
// # Why it needs the ground score
//
// Reconcile refuses without one. The specification's own weight table drops
// the ground signal to 0.20 when the video is poor and leans on satellite
// instead, and this platform has a hard rule that satellite cannot measure
// damage — Verify discards any figure produced without ground imagery. A
// composite computed mostly from NDVI would be the very thing that rule
// exists to prevent, wearing a different name.

// Fusion is the specification's composite, kept beside the panel's figure.
type Fusion struct {
	// CDP is the Composite Damage Percentage, in [0,100]. Evidence about the
	// panel's answer, never itself the answer.
	CDP float64 `json:"composite_damage_percent"`

	// Confidence is the specification's CS, in [0,1].
	Confidence float64 `json:"confidence"`

	// Triage is the tier the composite alone would route to.
	Triage string `json:"triage"`

	// The weights actually used, because they are chosen from a table by
	// conditions and an officer reading a composite deserves to see which
	// branch produced it.
	WeightGroundCV  float64 `json:"weight_ground_cv"`
	WeightSatellite float64 `json:"weight_satellite"`
	WeightTerrain   float64 `json:"weight_terrain"`
	WeightReason    string  `json:"weight_reason"`

	SCV  float64 `json:"s_cv"`
	SSat float64 `json:"s_sat"`
	SDEM float64 `json:"s_dem"`

	// InterModalDiff is |S_CV - S_Sat|: how far the ground and the sky
	// disagree about the same field. The specification's variance penalty.
	InterModalDiff float64 `json:"inter_modal_diff"`

	// CloudCover is the fraction of the optical record lost to cloud, which
	// selects the weight branch.
	CloudCover float64 `json:"cloud_cover"`
}

// FusionInput is what the reconciliation needs.
type FusionInput struct {
	// Ground is required. Without it there is nothing to reconcile that this
	// platform is willing to call damage.
	Ground *groundcv.Score

	// SSat is the satellite damage signal in [0,100]. RRVL is what the
	// platform has: how far the canopy fell toward bare soil.
	SSat float64

	// SDEM is the terrain hydrological risk in [0,100].
	SDEM float64

	// CloudCover in [0,1]: the share of the optical record lost.
	CloudCover float64
}

// Weight branches from the specification's dynamic assignment.
const (
	reasonGoodVideoClearSky = "good video, clear sky"
	reasonGoodVideoCloud    = "good video, cloud; radar carries the satellite term"
	reasonPoorVideo         = "poor video; the instruments carry it"
	reasonMixed             = "usable video, partial cloud"
)

// Reconcile computes the specification's composite beside the panel's figure.
//
// Returns nil where there is no ground score. That is the ordinary case until
// a vision pipeline exists, and it is reported as an absent cross-check rather
// than as a failure.
func Reconcile(in FusionInput) *Fusion {
	if in.Ground == nil {
		return nil
	}

	qCV := clamp(in.Ground.Quality, 0, 1)
	cloud := clamp(in.CloudCover, 0, 1)
	sCV := clamp(in.Ground.SCV, 0, 100)
	sSat := clamp(in.SSat, 0, 100)
	sDEM := clamp(in.SDEM, 0, 100)

	// The specification's table, transcribed. The second branch is the one
	// this whole exercise is about: when cloud has taken the optical signal
	// away, the ground video carries 0.65 of the weight and radar is what is
	// left of the satellite term.
	var wCV, wSat, wDEM float64
	var reason string
	switch {
	case qCV >= 0.85 && cloud < 0.20:
		wCV, wSat, wDEM, reason = 0.50, 0.35, 0.15, reasonGoodVideoClearSky
	case qCV >= 0.85 && cloud >= 0.20:
		wCV, wSat, wDEM, reason = 0.65, 0.15, 0.20, reasonGoodVideoCloud
	case qCV < 0.60:
		wCV, wSat, wDEM, reason = 0.20, 0.55, 0.25, reasonPoorVideo
	default:
		wCV, wSat, wDEM, reason = 0.35, 0.45, 0.20, reasonMixed
	}

	cdp := clamp(wCV*sCV+wSat*sSat+wDEM*sDEM, 0, 100)

	diff := math.Abs(sCV - sSat)
	confidence := clamp(qCV*(1-diff/100)*(1-0.4*cloud), 0, 1)

	f := &Fusion{
		CDP:             cdp,
		Confidence:      confidence,
		Triage:          triageForComposite(confidence, diff),
		WeightGroundCV:  wCV,
		WeightSatellite: wSat,
		WeightTerrain:   wDEM,
		WeightReason:    reason,
		SCV:             sCV,
		SSat:            sSat,
		SDEM:            sDEM,
		InterModalDiff:  diff,
		CloudCover:      cloud,
	}
	return f
}

// triageForComposite is the specification's automated routing.
func triageForComposite(confidence, interModalDiff float64) string {
	switch {
	case confidence >= 0.85 && interModalDiff <= 8.0:
		return TriageGreen
	case confidence < 0.60 || interModalDiff > 25.0:
		return TriageRed
	default:
		return TriageAmber
	}
}

// divergenceEscalation is how far the composite may sit from the published
// figure before a human is required.
//
// The same 25 points the specification uses for its own inter-modal check.
// Applied here between the composite and the panel rather than between two
// instruments, because that is the disagreement that decides a payment: if
// the arithmetic and the panel differ by a quarter of the whole scale about
// the same field, at least one of them is wrong.
const divergenceEscalation = 25.0

// Diverges reports whether the composite disagrees with the published figure
// far enough to need a person.
//
// False when either is absent: a missing cross-check is not a disagreement,
// and must not escalate a claim on its own.
func (f *Fusion) Diverges(published *float64) bool {
	if f == nil || published == nil {
		return false
	}
	return math.Abs(f.CDP-*published) > divergenceEscalation
}
