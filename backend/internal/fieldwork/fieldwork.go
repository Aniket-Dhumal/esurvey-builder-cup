// Package fieldwork turns what an officer counted into what they can defend.
//
// # The gap this closes
//
// An officer records a percentage and a paragraph. The paragraph is required
// and genuinely useful; the percentage behind it rests on nothing checkable.
// It is a judgement written as a number, and at an appeal there is no way to
// tell a careful one from a hurried one.
//
// A quadrat changes that. Counting plants in a square metre against what
// should be there, counting how many are lodged, and counting damaged pods in
// a sample are objective, quick, and reproducible by the next officer to
// stand in the same field.
//
// # Why the counts are stored and the percentages are not
//
// Percentages are derived here from the counts, so an officer sees arithmetic
// rather than a number, the derivation can be corrected without rewriting
// history, and an adjudicator can check it. A stored percentage whose inputs
// are gone is exactly what this replaces.
//
// # What this does not do
//
// It does not produce the damage figure. Stand loss, lodging and organ damage
// are three different things and they do not add up to a yield loss without
// agronomy this platform does not have. What they give an officer is a
// defensible floor and a defensible ceiling, and the reasoning between them
// stays the officer's.
package fieldwork

import (
	"fmt"
	"math"
)

// Measurement is one quadrat, as recorded.
type Measurement struct {
	ID       string `json:"id"`
	ReportID string `json:"report_id"`
	LineNo   int    `json:"line_no,omitempty"`

	RecordedBy   string `json:"recorded_by"`
	RecordedName string `json:"recorded_by_name,omitempty"`
	RecordedAt   string `json:"recorded_at"`

	QuadratSideM float64 `json:"quadrat_side_m"`

	PlantsExpected *int `json:"plants_expected,omitempty"`
	PlantsPresent  *int `json:"plants_present,omitempty"`
	PlantsLodged   *int `json:"plants_lodged,omitempty"`

	OrganSampled *int `json:"organ_sampled,omitempty"`
	OrganDamaged *int `json:"organ_damaged,omitempty"`

	SoilType string `json:"soil_type,omitempty"`
	Notes    string `json:"notes,omitempty"`

	CaptureID string `json:"capture_id,omitempty"`
}

// AreaM2 is the ground the counts were taken over.
func (m *Measurement) AreaM2() float64 { return m.QuadratSideM * m.QuadratSideM }

// Derived is what the counts imply, with the arithmetic visible.
type Derived struct {
	// StandLossPercent is plants missing against plants expected. The most
	// objective figure available in a field, and the one an appeal can check.
	StandLossPercent *float64 `json:"stand_loss_percent,omitempty"`

	// LodgedPercent is of the plants that are there. A lodged plant is not a
	// missing plant and the two must not be added.
	LodgedPercent *float64 `json:"lodged_percent,omitempty"`

	// OrganDamagePercent is damaged pods, heads or bolls in the sample.
	OrganDamagePercent *float64 `json:"organ_damage_percent,omitempty"`

	// PlantsPerM2 lets one quadrat be compared with another taken over a
	// different frame.
	PlantsPerM2 *float64 `json:"plants_per_m2,omitempty"`

	// Working is the arithmetic in words, so an officer reads a derivation
	// rather than a result.
	Working []string `json:"working,omitempty"`

	// Caveats are what the counts cannot support.
	Caveats []string `json:"caveats,omitempty"`
}

// Derive computes what a measurement implies.
func Derive(m *Measurement) Derived {
	var d Derived
	if m == nil {
		return d
	}

	if m.PlantsExpected != nil && m.PlantsPresent != nil && *m.PlantsExpected > 0 {
		expected, present := float64(*m.PlantsExpected), float64(*m.PlantsPresent)
		loss := clampPercent((expected - present) / expected * 100)
		d.StandLossPercent = &loss
		d.Working = append(d.Working, fmt.Sprintf(
			"stand loss: %d expected less %d present, over %d expected = %.0f%%",
			*m.PlantsExpected, *m.PlantsPresent, *m.PlantsExpected, loss))

		if present > expected {
			d.Caveats = append(d.Caveats, fmt.Sprintf(
				"more plants were counted (%d) than the spacing expects (%d), so "+
					"the expected figure may be for a different spacing than the "+
					"farmer used; stand loss is reported as nil rather than negative",
				*m.PlantsPresent, *m.PlantsExpected))
		}
	}

	if m.PlantsPresent != nil && m.PlantsLodged != nil && *m.PlantsPresent > 0 {
		pct := clampPercent(float64(*m.PlantsLodged) / float64(*m.PlantsPresent) * 100)
		d.LodgedPercent = &pct
		d.Working = append(d.Working, fmt.Sprintf(
			"lodging: %d lodged of %d standing = %.0f%%",
			*m.PlantsLodged, *m.PlantsPresent, pct))
	}

	if m.OrganSampled != nil && m.OrganDamaged != nil && *m.OrganSampled > 0 {
		pct := clampPercent(float64(*m.OrganDamaged) / float64(*m.OrganSampled) * 100)
		d.OrganDamagePercent = &pct
		d.Working = append(d.Working, fmt.Sprintf(
			"organ damage: %d damaged of %d sampled = %.0f%%",
			*m.OrganDamaged, *m.OrganSampled, pct))

		if *m.OrganSampled < minSample {
			d.Caveats = append(d.Caveats, fmt.Sprintf(
				"only %d were sampled; a share taken from fewer than %d moves by "+
					"more than %.0f points with a single item",
				*m.OrganSampled, minSample, 100/float64(*m.OrganSampled)))
		}
	}

	if m.PlantsPresent != nil && m.AreaM2() > 0 {
		per := round1(float64(*m.PlantsPresent) / m.AreaM2())
		d.PlantsPerM2 = &per
	}

	if m.CaptureID == "" {
		d.Caveats = append(d.Caveats,
			"no photograph of the quadrat was attached, so these counts are "+
				"testimony rather than something a second officer could check")
	}

	if m.AreaM2() < 1 {
		d.Caveats = append(d.Caveats, fmt.Sprintf(
			"the quadrat was %.2f m across, under a square metre; a small frame "+
				"placed where damage is worst reads high, and placed where it is "+
				"lightest reads low", m.QuadratSideM))
	}

	return d
}

// minSample is the count below which a percentage is more arithmetic than
// evidence. Twenty: below it a single pod moves the figure by five points.
const minSample = 20

// Summary is what a set of quadrats says about one crop line.
type Summary struct {
	Quadrats int `json:"quadrats"`
	// TotalAreaM2 is how much ground was actually looked at. Two square
	// metres of a two-hectare field is a sample, and saying so is the honest
	// framing for everything below.
	TotalAreaM2 float64 `json:"total_area_m2"`

	MeanStandLoss    *float64 `json:"mean_stand_loss_percent,omitempty"`
	MeanLodged       *float64 `json:"mean_lodged_percent,omitempty"`
	MeanOrganDamage  *float64 `json:"mean_organ_damage_percent,omitempty"`
	SpreadStandLoss  *float64 `json:"stand_loss_spread_points,omitempty"`
	WithPhotographs  int      `json:"with_photographs"`
	SupportsAFigure  bool     `json:"supports_a_figure"`
	SamplingFraction string   `json:"sampling_fraction,omitempty"`

	Caveats []string `json:"caveats,omitempty"`
}

// minQuadrats is how many frames make a sample rather than a spot check.
//
// Three, placed apart. One quadrat says what one square metre looked like;
// the value of three is that their disagreement is itself information about
// how uneven the damage was.
const minQuadrats = 3

// Summarise combines quadrats for one crop line.
func Summarise(ms []*Measurement, fieldAreaHa float64) Summary {
	s := Summary{Quadrats: len(ms)}
	if len(ms) == 0 {
		s.Caveats = append(s.Caveats,
			"no measurements were taken, so the officer's figure rests on visual "+
				"judgement alone")
		return s
	}

	var stand, lodged, organ []float64
	for _, m := range ms {
		s.TotalAreaM2 += m.AreaM2()
		if m.CaptureID != "" {
			s.WithPhotographs++
		}
		d := Derive(m)
		if d.StandLossPercent != nil {
			stand = append(stand, *d.StandLossPercent)
		}
		if d.LodgedPercent != nil {
			lodged = append(lodged, *d.LodgedPercent)
		}
		if d.OrganDamagePercent != nil {
			organ = append(organ, *d.OrganDamagePercent)
		}
	}
	s.TotalAreaM2 = round1(s.TotalAreaM2)

	if m := mean(stand); m != nil {
		s.MeanStandLoss = m
		if sp := spread(stand); sp != nil {
			s.SpreadStandLoss = sp
			if *sp > unevenPoints {
				s.Caveats = append(s.Caveats, fmt.Sprintf(
					"the quadrats disagree by %.0f points on stand loss, which says "+
						"the damage is uneven across the field rather than that any "+
						"of them is wrong", *sp))
			}
		}
	}
	s.MeanLodged = mean(lodged)
	s.MeanOrganDamage = mean(organ)

	if fieldAreaHa > 0 && s.TotalAreaM2 > 0 {
		fraction := s.TotalAreaM2 / (fieldAreaHa * 10_000) * 100
		s.SamplingFraction = fmt.Sprintf(
			"%.1f m² sampled from %.2f ha (%.3f%% of the field)",
			s.TotalAreaM2, fieldAreaHa, fraction)
	}

	s.SupportsAFigure = len(ms) >= minQuadrats && (len(stand) > 0 || len(organ) > 0)

	if len(ms) < minQuadrats {
		s.Caveats = append(s.Caveats, fmt.Sprintf(
			"%d quadrat(s) were taken. One says what one square metre looked "+
				"like; %d placed apart let their disagreement show how uneven the "+
				"damage is", len(ms), minQuadrats))
	}
	if s.WithPhotographs < len(ms) {
		s.Caveats = append(s.Caveats, fmt.Sprintf(
			"%d of %d quadrats have no photograph attached",
			len(ms)-s.WithPhotographs, len(ms)))
	}

	return s
}

// unevenPoints is the disagreement between quadrats that says the field is
// patchy rather than that somebody miscounted.
const unevenPoints = 25

func mean(v []float64) *float64 {
	if len(v) == 0 {
		return nil
	}
	var sum float64
	for _, x := range v {
		sum += x
	}
	out := round1(sum / float64(len(v)))
	return &out
}

// spread is the range, not the standard deviation.
//
// Three quadrats do not support a standard deviation, and the range is what
// an officer can check against what they remember seeing.
func spread(v []float64) *float64 {
	if len(v) < 2 {
		return nil
	}
	lo, hi := v[0], v[0]
	for _, x := range v {
		lo, hi = math.Min(lo, x), math.Max(hi, x)
	}
	out := round1(hi - lo)
	return &out
}

func clampPercent(v float64) float64 {
	return round1(math.Max(0, math.Min(100, v)))
}

func round1(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}
