// Package sar reads radar backscatter, for the days optical imagery cannot
// see at all.
//
// # The blindness this closes
//
// Monsoon calamity happens under cloud. Sentinel-2 is optical, so the NDVI
// series goes blank at exactly the moment it is most needed -- the platform
// already records largest_gap_days, which means it knows when it cannot see
// and has had nothing to fall back on. A two-month gap over a kharif season
// is ordinary, not exceptional.
//
// Radar does not care about cloud. Sentinel-1 passes every six to twelve days
// over Maharashtra whatever the sky is doing, and it is free.
//
// # What radar sees that NDVI cannot
//
// Two things this platform currently infers rather than observes.
//
// Standing water is near-black in radar: a flat water surface reflects the
// pulse away from the satellite instead of back to it, so flooding shows as a
// sharp collapse in backscatter. Waterlogging risk today is derived from
// terrain slope, which says where water would collect, not where it did.
//
// Lodging changes the geometry of the canopy. A standing cereal scatters from
// vertical stems and returns a strong cross-polarised signal; the same crop
// flattened returns much less of it while the co-polarised return holds up or
// rises. The ratio between them falls, and that fall is the most direct
// measurement of lodging available from orbit.
//
// # What it is not
//
// Backscatter is not damage. It is a physical measurement whose relationship
// to yield loss depends on crop, growth stage, soil moisture and incidence
// angle, and this package states findings about the field's condition rather
// than percentages. Nothing here may produce a damage figure on its own, for
// the same reason nothing else in this platform may.
package sar

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Observation is one Sentinel-1 pass over a parcel, as decibels.
//
// VV is co-polarised (sent vertical, received vertical) and VH cross-
// polarised. Both are needed: either alone is ambiguous between the two
// things worth detecting.
type Observation struct {
	Date string  `json:"date"`
	VVdB float64 `json:"vv_db"`
	VHdB float64 `json:"vh_db"`

	// Orbit distinguishes ascending from descending passes. Backscatter
	// differs by several decibels between them for the same unchanged field,
	// so comparing across orbits produces changes that did not happen.
	Orbit string `json:"orbit,omitempty"`
}

// Ratio is VH minus VV in decibels, which is the ratio in linear units.
//
// The lodging indicator. A standing stem canopy depolarises the pulse and
// lifts VH relative to VV; flatten it and the ratio falls.
func (o Observation) Ratio() float64 { return o.VHdB - o.VVdB }

// Finding is one thing the radar record says about the field.
type Finding struct {
	Code       string   `json:"code"`
	Confidence string   `json:"confidence"`
	Detail     string   `json:"detail"`
	Caveats    []string `json:"caveats,omitempty"`
}

// Finding codes.
const (
	// FindingInundation is standing water on the parcel: a collapse in
	// backscatter to values only a smooth surface produces.
	FindingInundation = "inundation"
	// FindingLodging is a flattened canopy: the cross-polarised return falls
	// away while the co-polarised return holds.
	FindingLodging = "lodging"
	// FindingCanopyLoss is a general fall in scattering consistent with the
	// crop being removed or destroyed, without the signature of either of the
	// above.
	FindingCanopyLoss = "canopy_loss"
	// FindingNoChange is worth reporting as loudly as the others: radar saw
	// the field through the cloud and it did not change.
	FindingNoChange = "no_material_change"
)

// Confidence levels.
const (
	ConfidenceHigh     = "high"
	ConfidenceModerate = "moderate"
	ConfidenceLow      = "low"
)

// Thresholds in decibels, named so they can be argued with rather than found
// inside a conditional.
const (
	// waterVV is the co-polarised return below which a surface is behaving
	// like open water rather than like a field. Published ranges put flooded
	// land between -15 and -22 dB and vegetated land above -12.
	waterVV = -15.0

	// floodDropVV is the fall from the parcel's own pre-incident level that
	// counts as inundation rather than as drier soil. Absolute thresholds
	// alone misclassify sandy and freshly tilled ground.
	floodDropVV = 3.0

	// lodgingRatioDrop is the fall in VH minus VV that indicates a canopy has
	// gone from vertical to flat.
	lodgingRatioDrop = 1.5

	// canopyDropVH is a fall in the cross-polarised return large enough to
	// mean the scattering volume is gone.
	canopyDropVH = 2.0

	// noiseDB is the change below which two passes are the same field.
	// Sentinel-1 radiometric stability is well inside this; the margin is for
	// soil moisture, which moves backscatter by a decibel or two on its own.
	noiseDB = 1.0
)

// Reading is what the radar record says across an incident.
type Reading struct {
	Before []Observation `json:"before"`
	After  []Observation `json:"after"`

	BaselineVV    float64 `json:"baseline_vv_db"`
	BaselineVH    float64 `json:"baseline_vh_db"`
	BaselineRatio float64 `json:"baseline_ratio_db"`

	IncidentVV    float64 `json:"incident_vv_db"`
	IncidentVH    float64 `json:"incident_vh_db"`
	IncidentRatio float64 `json:"incident_ratio_db"`

	DeltaVV    float64 `json:"delta_vv_db"`
	DeltaVH    float64 `json:"delta_vh_db"`
	DeltaRatio float64 `json:"delta_ratio_db"`

	// FirstAfter is the date of the first pass after the incident. Radar
	// revisit is six to twelve days, so a flood that drained before the pass
	// leaves no trace, and the officer needs the gap to know that.
	FirstAfter    string `json:"first_after,omitempty"`
	DaysToFirst   int    `json:"days_to_first_pass"`
	OrbitsMatched bool   `json:"orbits_matched"`

	Findings []Finding `json:"findings"`

	// Missions names the collections the series actually came from.
	//
	// Carried so a reading can be traced to a satellite rather than to
	// "radar". The radar service can also generate a plausible series with no
	// satellite behind it, for developing this side of the contract, and it
	// labels those SYNTHETIC here.
	Missions []string `json:"missions,omitempty"`

	// Synthetic means no satellite observed this parcel and these decibels
	// were generated.
	//
	// The radar service has always labelled them. Nothing on this side read
	// the label: the missions list was dropped on the floor at the point the
	// series was converted, so a generated reading arrived in the evidence
	// packet and in the dossier indistinguishable from an observation, under
	// a heading that says Sentinel-1 and in a row marked as corroborating.
	// That is a fabricated finding on a record that decides compensation.
	Synthetic bool `json:"synthetic,omitempty"`
}

// SyntheticMission is what the radar service puts in the missions list when it
// generated the series rather than observing it.
const SyntheticMission = "SYNTHETIC"

// IsSynthetic reports whether a series was generated rather than observed.
//
// Any occurrence is enough. A mixed list would mean part of the series was
// invented, and a reading that is partly invented is not an observation.
func IsSynthetic(missions []string) bool {
	for _, m := range missions {
		if strings.EqualFold(strings.TrimSpace(m), SyntheticMission) {
			return true
		}
	}
	return false
}

// Analyse compares the passes before an incident with those after it.
//
// Returns a reason rather than a Reading when the record cannot support one.
// That is a real outcome and not a failure: a parcel with one pass either
// side of a hailstorm has not been observed enough to say anything, and
// saying so is more useful than a confident reading of noise.
func Analyse(obs []Observation, incidentDate string) (*Reading, string, bool) {
	incident, err := time.Parse(dayLayout, incidentDate)
	if err != nil {
		return nil, fmt.Sprintf("incident date %q is not a date", incidentDate), false
	}

	clean := make([]Observation, 0, len(obs))
	for _, o := range obs {
		if _, err := time.Parse(dayLayout, o.Date); err != nil {
			continue
		}
		if math.IsNaN(o.VVdB) || math.IsNaN(o.VHdB) {
			continue
		}
		// Backscatter outside this range is not a field; it is a corrupted
		// value or a scene that failed calibration.
		if o.VVdB < -35 || o.VVdB > 5 || o.VHdB < -40 || o.VHdB > 0 {
			continue
		}
		clean = append(clean, o)
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].Date < clean[j].Date })

	r := &Reading{}
	for _, o := range clean {
		t, _ := time.Parse(dayLayout, o.Date)
		if t.After(incident) {
			r.After = append(r.After, o)
			continue
		}
		r.Before = append(r.Before, o)
	}

	if len(r.Before) == 0 {
		return nil, "no radar pass precedes the incident, so there is nothing to " +
			"compare the field against", false
	}
	if len(r.After) == 0 {
		return nil, "no radar pass follows the incident yet; Sentinel-1 revisits " +
			"every six to twelve days", false
	}

	// Same orbit direction on both sides where possible. Backscatter differs
	// by several decibels between ascending and descending passes over an
	// unchanged field, so mixing them manufactures a change.
	before, after, matched := matchOrbits(r.Before, r.After)
	r.OrbitsMatched = matched

	r.BaselineVV, r.BaselineVH = meanVV(before), meanVH(before)
	r.BaselineRatio = round1(r.BaselineVH - r.BaselineVV)

	first := after[0]
	r.IncidentVV, r.IncidentVH = round1(first.VVdB), round1(first.VHdB)
	r.IncidentRatio = round1(first.Ratio())
	r.FirstAfter = first.Date

	firstDay, _ := time.Parse(dayLayout, first.Date)
	r.DaysToFirst = int(firstDay.Sub(incident).Hours() / 24)

	r.DeltaVV = round1(r.IncidentVV - r.BaselineVV)
	r.DeltaVH = round1(r.IncidentVH - r.BaselineVH)
	r.DeltaRatio = round1(r.IncidentRatio - r.BaselineRatio)

	r.Findings = findingsFor(r)
	return r, "", true
}

func findingsFor(r *Reading) []Finding {
	var out []Finding

	shared := func(f *Finding) {
		if !r.OrbitsMatched {
			f.Caveats = append(f.Caveats,
				"the passes either side of the incident are from different orbit "+
					"directions, which shifts backscatter by a few decibels over an "+
					"unchanged field; part of the change below may be geometry")
		}
		if r.DaysToFirst > 12 {
			f.Caveats = append(f.Caveats, fmt.Sprintf(
				"the first radar pass came %d days after the incident, so a "+
					"condition that resolved in the meantime -- water that drained, "+
					"a crop that was cut -- would not appear here", r.DaysToFirst))
		}
		if len(r.Before) < 2 {
			f.Caveats = append(f.Caveats,
				"one pass established the baseline, so soil moisture on that day "+
					"is carried into every figure above")
		}
	}

	switch {
	case r.IncidentVV <= waterVV && r.DeltaVV <= -floodDropVV:
		f := Finding{
			Code:       FindingInundation,
			Confidence: ConfidenceHigh,
			Detail: fmt.Sprintf(
				"the co-polarised return fell %.1f dB to %.1f dB, which is the "+
					"signature of a smooth water surface reflecting the pulse away "+
					"from the satellite. This is standing water observed, not "+
					"waterlogging inferred from slope.",
				-r.DeltaVV, r.IncidentVV),
		}
		if r.DeltaVH > -1 {
			f.Confidence = ConfidenceModerate
			f.Caveats = append(f.Caveats,
				"the cross-polarised return did not fall with it, which is "+
					"unusual for full submergence and may mean the crop is standing "+
					"in water rather than under it")
		}
		shared(&f)
		out = append(out, f)

	case r.DeltaRatio <= -lodgingRatioDrop && r.DeltaVV > -floodDropVV:
		f := Finding{
			Code:       FindingLodging,
			Confidence: ConfidenceModerate,
			Detail: fmt.Sprintf(
				"the ratio between the cross- and co-polarised returns fell %.1f dB "+
					"while the co-polarised return held. A standing stem canopy "+
					"depolarises the pulse; a flattened one does not, so this is "+
					"consistent with the crop having gone down.",
				-r.DeltaRatio),
		}
		if r.DeltaVH <= -canopyDropVH {
			f.Confidence = ConfidenceHigh
		}
		shared(&f)
		out = append(out, f)

	case r.DeltaVH <= -canopyDropVH:
		f := Finding{
			Code:       FindingCanopyLoss,
			Confidence: ConfidenceModerate,
			Detail: fmt.Sprintf(
				"the cross-polarised return fell %.1f dB without the signature of "+
					"either flooding or lodging, which is consistent with the "+
					"scattering volume being gone -- a crop destroyed, cut or "+
					"stripped.", -r.DeltaVH),
			Caveats: []string{
				"harvest produces this signature too, and radar cannot tell a " +
					"harvested field from a destroyed one",
			},
		}
		shared(&f)
		out = append(out, f)

	case math.Abs(r.DeltaVV) < noiseDB && math.Abs(r.DeltaVH) < noiseDB:
		f := Finding{
			Code:       FindingNoChange,
			Confidence: ConfidenceHigh,
			Detail: fmt.Sprintf(
				"radar saw this field through the cloud on %s and it is scattering "+
					"as it did before the incident (%.1f dB against %.1f dB). "+
					"Whatever the optical record could not see, the field itself "+
					"did not change.", r.FirstAfter, r.IncidentVV, r.BaselineVV),
		}
		shared(&f)
		out = append(out, f)

	default:
		f := Finding{
			Code:       FindingNoChange,
			Confidence: ConfidenceLow,
			Detail: fmt.Sprintf(
				"backscatter moved by %.1f dB co-polarised and %.1f dB cross-"+
					"polarised, which is more than instrument noise and less than "+
					"any of the signatures this package recognises. Soil moisture "+
					"alone moves it this far.", r.DeltaVV, r.DeltaVH),
		}
		shared(&f)
		out = append(out, f)
	}

	return out
}

// matchOrbits pairs passes from the same orbit direction where the record
// allows, and reports whether it managed to.
func matchOrbits(before, after []Observation) ([]Observation, []Observation, bool) {
	if len(after) == 0 {
		return before, after, false
	}
	want := after[0].Orbit
	if want == "" {
		return before, after, false
	}

	var b []Observation
	for _, o := range before {
		if o.Orbit == want {
			b = append(b, o)
		}
	}
	if len(b) == 0 {
		return before, after, false
	}
	return b, after, true
}

// CoversOpticalGap reports whether the radar record has passes inside a
// window the optical series could not see.
//
// The question worth asking when NDVI comes back with a two-month hole: was
// anything watching at all.
func CoversOpticalGap(obs []Observation, from, to string) (int, bool) {
	start, err1 := time.Parse(dayLayout, from)
	end, err2 := time.Parse(dayLayout, to)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	var n int
	for _, o := range obs {
		t, err := time.Parse(dayLayout, o.Date)
		if err != nil {
			continue
		}
		if !t.Before(start) && !t.After(end) {
			n++
		}
	}
	return n, n > 0
}

const dayLayout = "2006-01-02"

func meanVV(o []Observation) float64 {
	var s float64
	for _, x := range o {
		s += x.VVdB
	}
	return round1(s / float64(len(o)))
}

func meanVH(o []Observation) float64 {
	var s float64
	for _, x := range o {
		s += x.VHdB
	}
	return round1(s / float64(len(o)))
}

func round1(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	return math.Round(v*10) / 10
}
