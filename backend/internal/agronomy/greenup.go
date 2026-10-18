package agronomy

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Reading the sowing date off the satellite record.
//
// # Why this matters more than it sounds
//
// Sowing date arrives as a farmer's declaration or from a registry field, and
// nothing verifies it. Every phenological conclusion is built on it: the
// growth stage, what the calamity would have cost at that stage, whether the
// damage mode is even possible. The phenology specialist has already said so
// in live runs -- it reports that it inherits uncertainty from a sowing date
// nobody checked.
//
// A crop announces its own emergence. NDVI sits at bare-soil values until the
// canopy closes, then climbs steeply. The date it starts climbing is an
// observation, not a declaration, and it costs nothing: the series is already
// fetched for every assessment.
//
// # What this is not
//
// It is not a fraud detector on its own. A fortnight's disagreement between
// declared and observed is worth an officer's attention; it is not proof of
// anything, because transplanted crops, re-sowing after a failed germination,
// and a cloud gap over the emergence window all produce the same gap
// honestly. The output says which of those it cannot rule out.

// Observation is one cloud-free NDVI reading.
//
// Declared here rather than taken from the satellite package so this stays a
// pure function over numbers: it is the piece of the pipeline most worth
// testing exhaustively, and it should not need a client to do it.
type Observation struct {
	Date string
	NDVI float64
}

// Confidence in a derived figure.
const (
	ConfidenceHigh     = "high"
	ConfidenceModerate = "moderate"
	ConfidenceLow      = "low"
)

// GreenUp is what the series says about emergence.
type GreenUp struct {
	// Date is the interpolated crossing, in YYYY-MM-DD.
	Date string `json:"date"`

	Confidence string `json:"confidence"`

	// NDVIFloor and NDVIPeak bound the season; Amplitude is the climb. A
	// small amplitude means there was never much of a canopy, which is itself
	// worth knowing.
	NDVIFloor float64 `json:"ndvi_floor"`
	NDVIPeak  float64 `json:"ndvi_peak"`
	Amplitude float64 `json:"amplitude"`

	// ObservationsBefore is how many readings bracket the crossing from the
	// left. One is a guess; several is a measurement.
	ObservationsBefore int `json:"observations_before"`

	// BracketGapDays is the interval the crossing was interpolated across.
	// A crossing placed inside a five-day gap is worth something; one placed
	// inside a fifty-day monsoon gap is arithmetic, not evidence.
	BracketGapDays int `json:"bracket_gap_days"`

	// Caveats are the honest reasons this could be wrong even when it looks
	// clean, in words an officer can read.
	Caveats []string `json:"caveats,omitempty"`
}

// greenUpFraction is where emergence is taken to be, as a share of the
// season's climb. Twenty per cent of amplitude is the conventional threshold
// in phenology work and is used here for the same reason the rest of this
// package prefers tables: it is a published choice somebody can disagree with.
const greenUpFraction = 0.20

// minAmplitude is the smallest climb that counts as a crop growing.
//
// Below this the field never developed a canopy distinguishable from bare
// soil -- a failed sowing, a fallow parcel, a series that is all cloud
// shoulder, or a boundary that does not sit on the field it claims to.
const minAmplitude = 0.15

// DetectGreenUp finds the emergence date implied by an NDVI series.
//
// Returns false with a reason when the series cannot support the answer. That
// is the common case in kharif, and reporting it is the point: an assessment
// that says "the sowing date could not be checked because the field was under
// cloud from June to August" is more useful than one that quietly checked
// nothing.
func DetectGreenUp(series []Observation) (GreenUp, string, bool) {
	clean := make([]Observation, 0, len(series))
	for _, o := range series {
		if _, err := parseDay(o.Date); err != nil {
			continue
		}
		if math.IsNaN(o.NDVI) || math.IsInf(o.NDVI, 0) {
			continue
		}
		clean = append(clean, o)
	}
	if len(clean) < 3 {
		return GreenUp{}, fmt.Sprintf(
			"only %d usable observations; emergence cannot be bracketed", len(clean)), false
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].Date < clean[j].Date })

	peakIdx := 0
	for i, o := range clean {
		if o.NDVI > clean[peakIdx].NDVI {
			peakIdx = i
		}
	}
	if peakIdx == 0 {
		return GreenUp{}, "the series peaks at its first observation; " +
			"emergence happened before the window starts", false
	}

	floorIdx := 0
	for i := 0; i <= peakIdx; i++ {
		if clean[i].NDVI < clean[floorIdx].NDVI {
			floorIdx = i
		}
	}
	if floorIdx >= peakIdx {
		return GreenUp{}, "no rise precedes the peak", false
	}

	floor, peak := clean[floorIdx].NDVI, clean[peakIdx].NDVI
	amplitude := peak - floor
	if amplitude < minAmplitude {
		return GreenUp{}, fmt.Sprintf(
			"the series rises only %.2f NDVI; no canopy distinguishable from "+
				"bare soil developed on this boundary", amplitude), false
	}

	threshold := floor + greenUpFraction*amplitude

	// The first crossing after the floor. Searched forward from the floor
	// rather than backward from the peak: a mid-season dip that recovers
	// would otherwise be read as emergence.
	crossIdx := -1
	for i := floorIdx + 1; i <= peakIdx; i++ {
		if clean[i].NDVI >= threshold {
			crossIdx = i
			break
		}
	}
	if crossIdx <= 0 {
		return GreenUp{}, "no observation crosses the emergence threshold", false
	}

	prev, cur := clean[crossIdx-1], clean[crossIdx]
	prevDay, _ := parseDay(prev.Date)
	curDay, _ := parseDay(cur.Date)
	gap := int(curDay.Sub(prevDay).Hours() / 24)

	// Linear interpolation between the bracketing passes. The truth is a
	// sigmoid, but fitting one to four points in a cloudy kharif would be
	// precision the data does not carry.
	frac := 0.0
	if span := cur.NDVI - prev.NDVI; span > 0 {
		frac = (threshold - prev.NDVI) / span
	}
	frac = math.Max(0, math.Min(1, frac))
	crossing := prevDay.AddDate(0, 0, int(math.Round(frac*float64(gap))))

	g := GreenUp{
		Date:               crossing.Format(dayLayout),
		NDVIFloor:          round3(floor),
		NDVIPeak:           round3(peak),
		Amplitude:          round3(amplitude),
		ObservationsBefore: crossIdx,
		BracketGapDays:     gap,
	}

	// Confidence is the width of the gap the crossing was interpolated
	// across, with a hard floor when a single pass precedes the rise: one
	// observation does not establish a bare-soil baseline to rise from.
	switch {
	case gap > 30 || crossIdx < 2:
		g.Confidence = ConfidenceLow
	case gap > 12:
		g.Confidence = ConfidenceModerate
	default:
		g.Confidence = ConfidenceHigh
	}

	if gap > 12 {
		g.Caveats = append(g.Caveats, fmt.Sprintf(
			"emergence was placed inside a %d-day gap between usable passes, so "+
				"the date could be out by up to that much", gap))
	}
	if crossIdx < 2 {
		g.Caveats = append(g.Caveats,
			"only one pass precedes the rise, so the field may already have been "+
				"green when the window opened")
	}
	if amplitude < 0.30 {
		g.Caveats = append(g.Caveats, fmt.Sprintf(
			"the canopy only reached %.2f NDVI above bare soil, which is a thin "+
				"crop or a boundary that includes ground outside the field", amplitude))
	}

	return g, "", true
}

// SowingCheck compares a declared sowing date against the observed emergence.
type SowingCheck struct {
	DeclaredDate string  `json:"declared_date"`
	ObservedDate string  `json:"observed_greenup_date"`
	DifferenceD  int     `json:"difference_days"`
	Agrees       bool    `json:"agrees"`
	Confidence   string  `json:"confidence"`
	Note         string  `json:"note"`
	GreenUp      GreenUp `json:"greenup"`
}

// emergenceLagDays is how long after sowing a canopy reaches the green-up
// threshold. It is a range, not a number: a transplanted crop is green on day
// one and a dryland crop waiting for rain may take three weeks.
const (
	emergenceLagMinDays = 5
	emergenceLagMaxDays = 25
)

// CheckSowingDate asks whether the declared date is consistent with what the
// satellite saw.
//
// Consistency, not equality. Sowing precedes green-up by days to weeks
// depending on crop, moisture and method, so the test is whether the declared
// date falls in a plausible window before the observed rise -- and a date
// *after* emergence is the interesting failure, because a crop cannot green
// up before it is sown.
func CheckSowingDate(declared string, series []Observation) (SowingCheck, bool) {
	d, err := parseDay(declared)
	if err != nil {
		return SowingCheck{}, false
	}
	g, _, ok := DetectGreenUp(series)
	if !ok {
		return SowingCheck{}, false
	}
	obs, err := parseDay(g.Date)
	if err != nil {
		return SowingCheck{}, false
	}

	lag := int(obs.Sub(d).Hours() / 24)

	c := SowingCheck{
		DeclaredDate: declared,
		ObservedDate: g.Date,
		DifferenceD:  lag,
		Confidence:   g.Confidence,
		GreenUp:      g,
	}

	switch {
	case lag < 0:
		c.Note = fmt.Sprintf(
			"the field was already greening %d days before the declared sowing date; "+
				"a crop cannot emerge before it is sown, so either the date is wrong "+
				"or the boundary covers a different field", -lag)
	case lag < emergenceLagMinDays:
		c.Note = fmt.Sprintf(
			"emergence appears only %d days after the declared sowing, which is fast "+
				"for a sown crop but ordinary for a transplanted one", lag)
		c.Agrees = true
	case lag <= emergenceLagMaxDays:
		c.Agrees = true
		c.Note = fmt.Sprintf(
			"emergence %d days after the declared sowing, which is the expected range", lag)
	default:
		c.Note = fmt.Sprintf(
			"emergence appears %d days after the declared sowing. A delayed "+
				"germination, a re-sowing after a failed one, or a mis-entered date "+
				"all produce this, and the record does not distinguish them", lag)
	}

	// A disagreement read off a low-confidence green-up is not a
	// disagreement. Saying so here stops the caller treating arithmetic
	// across a monsoon gap as a finding.
	if !c.Agrees && g.Confidence == ConfidenceLow {
		c.Note += "; the emergence date itself is low confidence, so this " +
			"difference is not evidence of anything on its own"
	}

	return c, true
}

const dayLayout = "2006-01-02"

func parseDay(s string) (time.Time, error) {
	return time.Parse(dayLayout, s)
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
