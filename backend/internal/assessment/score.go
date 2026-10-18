// Package assessment derives deterministic metrics from satellite and terrain
// evidence, and verifies what the Gemini agent returns.
//
// # Where the damage figure comes from
//
// Not from here. Sentinel-2 at 10 m over a one- or two-hectare smallholding
// reliably establishes whether green biomass was present and roughly when it
// grew. It cannot measure how badly a crop was damaged — a number derived from
// it alone would be a guess wearing a decimal point.
//
// So this package computes what arithmetic can settle exactly (relative
// vegetation loss, observation gaps, slope-derived waterlogging risk) and
// leaves the damage judgement to the agent, which reasons over ground imagery.
// Verify() in verify.go then re-checks the agent's answer.
//
// An earlier version of this file fused these metrics into a composite damage
// percentage. It was removed rather than kept alongside the agent: two
// competing damage models in one service is a defect waiting to be shipped.
package assessment

import (
	"fmt"
	"math"

	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// ndviBareSoil is the NDVI of dry unplanted soil in the target
// agro-ecological zone, per the specification's RRVL formulation. It is the
// floor against which vegetation loss is measured: a fall from 0.8 to 0.12 is
// total loss of green cover, not an 85% loss.
const ndviBareSoil = 0.12

// Severity tiers, from the specification's loss classification.
const (
	SeverityNegligible   = "Negligible"
	SeverityModerate     = "Moderate"
	SeveritySevere       = "Severe"
	SeverityCatastrophic = "Catastrophic"
)

// Triage tiers, from the dual-officer audit framework.
const (
	TriageGreen = "GREEN_FAST_TRACK"
	TriageAmber = "AMBER_STANDARD_AUDIT"
	TriageRed   = "RED_ANOMALY_ESCALATE"
)

// Satellite holds the deterministic figures derived from an NDVI series.
//
// These are evidence for the report and for fraud cross-checks — whether a
// crop grew, and whether the observed curve matches what was declared. They
// are not a damage measurement.
type Satellite struct {
	// BaselineNDVI is the highest observed reading in the window. It stands
	// in for the specification's phenology-adjusted expected curve, which
	// needs a confirmed crop type and multi-year history to fit.
	BaselineNDVI     float64 `json:"baseline_ndvi"`
	BaselineDate     string  `json:"baseline_date"`
	IncidentNDVI     float64 `json:"incident_ndvi"`
	IncidentDate     string  `json:"incident_date"`
	DeltaNDVI        float64 `json:"delta_ndvi"`
	RRVLPercent      float64 `json:"rrvl_percent"`
	ObservationCount int     `json:"observation_count"`

	// LargestGapDays is the longest run between cloud-free passes. A large
	// gap means the series may have missed both the canopy peak and the
	// damage event.
	LargestGapDays int `json:"largest_gap_days"`

	// VegetationDetected reports whether the series ever rose meaningfully
	// above bare soil. Nil when there are too few observations to say.
	VegetationDetected *bool `json:"vegetation_detected"`
}

// Terrain holds slope-derived waterlogging susceptibility.
//
// Susceptibility, not damage: flat ground under a thriving crop is not a loss.
// The specification reaches this through a Topographic Wetness Index; slope is
// the dominant term and is what the terrain engine gives us.
type Terrain struct {
	MeanSlopeDegrees float64 `json:"mean_slope_degrees"`
	Classification   string  `json:"classification"`
	WaterloggingRisk string  `json:"waterlogging_risk"`

	// The compass bearing the slope faces. In GIS terms that is the direction
	// of steepest descent, so it is the direction water runs -- which is what
	// makes it the axis worth sampling along, low end to high end.
	//
	// Nil where the parcel is too flat to have a downhill. The satellite
	// service returns null rather than inventing a direction, and an invented
	// one here would send a farmer confidently to the wrong end of their own
	// field.
	AspectDegrees *float64 `json:"aspect_degrees,omitempty"`

	// Highest point minus lowest, in metres. What decides whether there is a
	// high end to send anybody to: a bearing is meaningless across a parcel
	// with a hand's breadth of relief, and around Barshi that is common.
	ReliefMeters float64 `json:"relief_meters,omitempty"`
}

// minimumRelief is the elevation range below which a parcel has no usable
// downhill.
//
// A metre and a half across a whole field is within the error of a 30 m DEM
// resampled to a parcel this size. Below it the "low" end is noise, and
// sampling by geometry is not a worse answer -- it is the honest one.
const minimumRelief = 1.5

// DownhillBearing is the direction to sample towards, or nil where the parcel
// is too flat for that to mean anything.
//
// Callers pass the result straight to station placement, which treats nil as
// "space the stations along the long axis instead". The two are not different
// modes: the same four points come back either way, and only which end is
// called low changes.
func (t Terrain) DownhillBearing() *float64 {
	if t.AspectDegrees == nil || t.ReliefMeters < minimumRelief {
		return nil
	}
	return t.AspectDegrees
}

// vegetationPresentNDVI is the threshold above which green biomass is
// unambiguously present.
const vegetationPresentNDVI = 0.30

// ScoreSatellite derives the deterministic figures from an NDVI series.
//
// Returns an error on an empty series: a wholly clouded window is a successful
// analysis with nothing to say, and must not be reported as zero anything.
func ScoreSatellite(ts []satellite.NDVIPoint) (Satellite, error) {
	if len(ts) == 0 {
		return Satellite{}, fmt.Errorf("no cloud-free observations in the window")
	}

	s := Satellite{ObservationCount: len(ts)}

	peak := ts[0]
	for _, p := range ts[1:] {
		if p.NDVI > peak.NDVI {
			peak = p
		}
	}
	last := ts[len(ts)-1]

	s.BaselineNDVI = peak.NDVI
	s.BaselineDate = peak.Date
	s.IncidentNDVI = last.NDVI
	s.IncidentDate = last.Date
	s.DeltaNDVI = peak.NDVI - last.NDVI
	s.LargestGapDays = largestGapDays(ts)

	denom := peak.NDVI - ndviBareSoil
	if denom <= 0 {
		// Baseline at or below bare soil: nothing was growing to lose.
		s.RRVLPercent = 0
	} else {
		s.RRVLPercent = clamp((s.DeltaNDVI/denom)*100, 0, 100)
	}

	if len(ts) >= 2 {
		detected := peak.NDVI >= vegetationPresentNDVI
		s.VegetationDetected = &detected
	}

	return s, nil
}

// ScoreTerrain derives waterlogging susceptibility from slope. Flat land sheds
// no water.
func ScoreTerrain(t *satellite.TerrainResult) Terrain {
	slope := t.Slope.MeanDegrees

	var risk string
	switch {
	case slope < 1.0:
		risk = "Very High"
	case slope < 2.0:
		risk = "High"
	case slope < 5.0:
		risk = "Moderate"
	case slope < 10.0:
		risk = "Low"
	default:
		risk = "Very Low"
	}

	out := Terrain{
		MeanSlopeDegrees: slope,
		Classification:   t.Slope.Classification,
		WaterloggingRisk: risk,
		ReliefMeters:     t.Elevation.MaxMeters - t.Elevation.MinMeters,
	}

	// Carried through rather than derived from the slope: the service returns
	// null when the parcel is too flat to face anywhere, and that null is a
	// finding worth keeping. Copied rather than aliased so the assessment does
	// not hold a pointer into the satellite response.
	if t.Aspect.MeanDegrees != nil {
		deg := *t.Aspect.MeanDegrees
		out.AspectDegrees = &deg
	}

	return out
}

// RRVLPercent is the Relative Rate of Vegetation Loss, per the specification.
//
// Retained for the report, not as a damage measure: it quantifies how far the
// canopy fell toward bare soil, which is a useful thing to plot and a poor
// thing to pay out on.
func RRVLPercent(baselineNDVI, incidentNDVI float64) float64 {
	denom := baselineNDVI - ndviBareSoil
	if denom <= 0 {
		return 0
	}
	return clamp((baselineNDVI-incidentNDVI)/denom*100, 0, 100)
}

// largestGapDays returns the longest run in days between consecutive passes.
func largestGapDays(ts []satellite.NDVIPoint) int {
	if len(ts) < 2 {
		return 0
	}
	worst := 0
	for i := 1; i < len(ts); i++ {
		d, err := daysBetween(ts[i-1].Date, ts[i].Date)
		if err != nil {
			continue
		}
		if d > worst {
			worst = d
		}
	}
	return worst
}

func clamp(v, lo, hi float64) float64 {
	return math.Max(lo, math.Min(hi, v))
}
