// Package moisture reads canopy water and surface soil moisture.
//
// # What it is for
//
// Drought and dry spells are the calamities this platform is worst at. NDVI
// measures greenness, and a crop short of water stays green for days to weeks
// after it has started to fail -- by the time NDVI falls the loss has
// happened, and a harvested field and a dead one look the same. NDMI measures
// leaf water, which falls first. The Sentinel-1 soil record measures the top of
// the soil through the monsoon cloud that blinds both optical indices.
//
// # What it is not
//
// Neither is damage, and nothing in this package produces a percentage. An
// NDMI anomaly is a statement about leaf water relative to what this field and
// the fields around it normally hold; how much yield that costs depends on the
// crop and the stage, which the phenology evidence says and this does not. The
// dossier marks every row here as corroborating, and the fusion gives it no
// weight, until a season of officer findings shows whether it earns one.
//
// # Why every comparison is relative
//
// An NDMI of 0.2 is a stressed maize canopy and a healthy sorghum one. There is
// no absolute threshold, so there is none here. A value means something only
// against the same field in other years over the same calendar days, and
// against the cropland around it on the same day -- and the two comparisons
// answer different questions. Against its own history: is this field drier
// than it usually is? Against its neighbours: is it drier than the fields
// under the same sky? A field dry against both is the strongest statement this
// package makes. A field dry against its history and level with its neighbours
// is a dry locality, which corroborates an event rather than a parcel. A field
// wetter than all its neighbours in a dry spell is very probably irrigated,
// and a drought claim on it deserves a look.
//
// # Why the thresholds are constants and not tuned
//
// They are starting values from the literature on Sentinel-2 NDMI noise, set
// conservatively so that a finding is a change well outside what cloud residue
// and view angle produce on their own. They are named so that calibration
// against officer findings changes one line, and so that nobody can mistake
// them for measured truth in the meantime.
package moisture

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

const dayLayout = "2006-01-02"

// What makes a Sentinel-2 pass usable.
const (
	// MinClearFraction is the share of the parcel a pass must see clear. Below
	// it the mean is over a corner of the field, and a corner of the field is
	// not the claim.
	MinClearFraction = 0.5

	// MinClearPixels is nine 20 m pixels -- a 60 m square, about a third of a
	// hectare. Fewer is mostly the bund, the neighbour's crop and the edge of
	// a cloud mask.
	MinClearPixels = 9

	// MinRingPixels is how many clear cropland pixels the neighbourhood needs
	// before it is a comparison rather than one other field.
	MinRingPixels = 200
)

// How near the incident the comparison looks.
const (
	// NearIncidentDays is the half-width of the window around the incident in
	// which a pass counts as "at" it. Sentinel-2 revisits every five days and
	// the monsoon takes most of them, so anything narrower is empty in the
	// season this exists for.
	NearIncidentDays = 20

	// BeforeDays is how far before the incident a pass counts as the field's
	// state beforehand.
	BeforeDays = 45
)

// Thresholds. Starting values, deliberately conservative; see the package
// comment.
const (
	// NDMINoise is the change below which two NDMI figures are the same field.
	// Residual thin cloud, view angle and a different mix of pixels move a
	// parcel mean by a couple of hundredths on their own.
	NDMINoise = 0.04

	// NDMIMaterial is a change worth stating. Twice the noise.
	NDMIMaterial = 0.08

	// DenseCanopyNDVI is the greenness above which radar mostly sees the
	// canopy and not the soil, so the soil index stops being about soil.
	DenseCanopyNDVI = 0.6

	// SoilWet and SoilDry bound the relative soil index findings.
	SoilWet = 0.8
	SoilDry = 0.2

	// MinSoilHistory is how many passes on one relative orbit a dry floor and
	// wet ceiling need. Twenty is roughly eight months at a twelve-day revisit,
	// which is short of a full year and is the least that spans a dry season
	// and a monsoon.
	MinSoilHistory = 20
)

// Finding codes.
const (
	// FindingCanopyWaterStress is a field drier in leaf water than both its
	// own history and its neighbours.
	FindingCanopyWaterStress = "canopy_water_stress"
	// FindingLocalityDry is a field drier than its history and level with the
	// cropland around it: the whole locality is dry.
	FindingLocalityDry = "locality_dry"
	// FindingWetterThanNeighbours is a field holding markedly more leaf water
	// than the cropland around it. In a dry spell the usual reason is
	// irrigation.
	FindingWetterThanNeighbours = "wetter_than_neighbours"
	// FindingNoCanopyWaterChange is worth saying as plainly as a stress
	// finding: the field's leaf water is where it normally is.
	FindingNoCanopyWaterChange = "no_canopy_water_change"
	// FindingSurfaceWet is soil near the wettest this field gets on this
	// orbit.
	FindingSurfaceWet = "surface_wet"
	// FindingSurfaceDry is soil near the driest.
	FindingSurfaceDry = "surface_dry"
)

// Confidence levels, matching internal/sar.
const (
	ConfidenceHigh     = "high"
	ConfidenceModerate = "moderate"
	ConfidenceLow      = "low"
)

// Finding is one thing the moisture record says.
type Finding struct {
	Code       string   `json:"code"`
	Confidence string   `json:"confidence"`
	Detail     string   `json:"detail"`
	Caveats    []string `json:"caveats,omitempty"`
}

// Pass is one usable Sentinel-2 date in the season assessed.
type Pass struct {
	Date          string   `json:"date"`
	NDMI          float64  `json:"ndmi"`
	NDVI          *float64 `json:"ndvi,omitempty"`
	RingNDMI      *float64 `json:"ring_ndmi,omitempty"`
	ClearFraction float64  `json:"clear_fraction"`
}

// Canopy is the NDMI half of the reading.
type Canopy struct {
	// Season is every usable pass in the season assessed, for display.
	Season []Pass `json:"season"`

	// Before is the mean over passes in the BeforeDays leading up to the
	// incident, and Near the mean over passes within NearIncidentDays of it
	// on either side. Nil where no usable pass fell in the window.
	Before *float64 `json:"before_ndmi,omitempty"`
	Near   *float64 `json:"near_incident_ndmi,omitempty"`

	// NearPasses is how many passes the Near figure rests on.
	NearPasses int `json:"near_incident_passes"`

	// PriorMean is the field's own NDMI over the same calendar days in prior
	// seasons, as a mean of per-year means, and PriorSpread their standard
	// deviation where there are enough years to have one.
	PriorMean   *float64 `json:"prior_seasons_ndmi,omitempty"`
	PriorSpread *float64 `json:"prior_seasons_spread,omitempty"`
	PriorYears  int      `json:"prior_seasons_observed"`

	// Anomaly is Near minus PriorMean: this season against this field's
	// normal.
	Anomaly *float64 `json:"anomaly_vs_prior_seasons,omitempty"`

	// NeighbourGap is the field minus the cropland ring, averaged over the
	// passes near the incident that saw both. NeighbourGapBefore is the same
	// before the incident, so a field that was always wetter than its
	// neighbours is not read as having become so.
	NeighbourGap       *float64 `json:"gap_vs_neighbours,omitempty"`
	NeighbourGapBefore *float64 `json:"gap_vs_neighbours_before,omitempty"`
}

// Soil is the Sentinel-1 half of the reading.
type Soil struct {
	RelativeOrbit int     `json:"relative_orbit"`
	DryDB         float64 `json:"dry_reference_db"`
	WetDB         float64 `json:"wet_reference_db"`
	HistoryPasses int     `json:"history_passes"`

	// Index is where the reading nearest the incident sits between the dry
	// floor and the wet ceiling, 0 to 1.
	Index     *float64 `json:"index_near_incident,omitempty"`
	IndexDate string   `json:"index_date,omitempty"`

	// Before is the index at the last pass before the incident window, so a
	// field that was already wet is not read as having become so.
	Before     *float64 `json:"index_before,omitempty"`
	BeforeDate string   `json:"index_before_date,omitempty"`

	// CanopyObscured is set when the crop was dense enough that radar mostly
	// saw the canopy. The index is still reported, and labelled.
	CanopyObscured bool `json:"canopy_obscured"`
}

// Reading is the analysed moisture record.
type Reading struct {
	Canopy *Canopy `json:"canopy,omitempty"`
	Soil   *Soil   `json:"soil,omitempty"`

	Findings []Finding `json:"findings"`

	// Caveats that apply to the whole reading rather than to one finding.
	Caveats []string `json:"caveats,omitempty"`

	// Relevance says how much this evidence bears on the calamity claimed,
	// in a sentence an officer can use. NDMI is strong evidence for a dry
	// spell and weak evidence for a hailstorm, and a reader should not have
	// to know that already.
	Relevance string `json:"relevance"`

	ParcelPixels int      `json:"parcel_pixels"`
	RingPixels   int      `json:"ring_cropland_pixels"`
	Missions     []string `json:"missions,omitempty"`
	Synthetic    bool     `json:"synthetic,omitempty"`
}

// IsSynthetic reports whether a result was generated rather than observed.
func IsSynthetic(missions []string) bool {
	for _, m := range missions {
		if strings.EqualFold(m, "SYNTHETIC") {
			return true
		}
	}
	return false
}

// Window is the season a moisture request should cover around an incident.
//
// Sixty days before and thirty after. The before is longer because a dry spell
// is a slow event and its onset, not its end, is what the claim turns on; the
// after is long enough for a Sentinel-2 pass to find a gap in the cloud.
// Clipped to the sowing date, since leaf water before there was a crop is
// bare soil.
func Window(sowing, incident string) (string, string, bool) {
	inc, err := time.Parse(dayLayout, incident)
	if err != nil {
		return "", "", false
	}
	from := inc.AddDate(0, 0, -60)
	if s, err := time.Parse(dayLayout, sowing); err == nil && s.After(from) && s.Before(inc) {
		from = s
	}
	return from.Format(dayLayout), inc.AddDate(0, 0, 30).Format(dayLayout), true
}

// Analyse reads a measurement against an incident.
//
// Returns false with a reason when there is nothing to say, which the evidence
// packet records as an absent source rather than an empty reading: an officer
// should see "no clear Sentinel-2 pass near the incident" in the absent list,
// not a moisture section with no rows in it.
func Analyse(res *satellite.MoistureResult, incidentDate, calamity string) (*Reading, string, bool) {
	if res == nil {
		return nil, "no moisture measurement was returned", false
	}
	incident, err := time.Parse(dayLayout, incidentDate)
	if err != nil {
		return nil, fmt.Sprintf("incident date %q is not a date", incidentDate), false
	}

	r := &Reading{
		// An empty list, not nil. nil marshals as null, and every reader of
		// the packet -- the panel's prompt, the console, an appeal tool --
		// would have to know that null here means "none" rather than "not
		// computed".
		Findings:     []Finding{},
		ParcelPixels: res.ParcelPixels,
		RingPixels:   res.RingCroplandPixels,
		Missions:     res.Missions,
		Synthetic:    IsSynthetic(res.Missions),
		Relevance:    Relevance(calamity),
	}

	if res.ParcelPixels > 0 && res.ParcelPixels < MinClearPixels {
		r.Caveats = append(r.Caveats, fmt.Sprintf(
			"the parcel is %d Sentinel-2 pixels at 20 m, fewer than the %d a "+
				"parcel mean needs; its canopy figures are mostly edge and are "+
				"not read", res.ParcelPixels, MinClearPixels))
	} else {
		r.Canopy = canopy(res, incident)
	}

	r.Soil = soil(res.Soil, incident, r.Canopy)

	if r.Canopy == nil && r.Soil == nil {
		reason := "no clear Sentinel-2 pass and no Sentinel-1 history covered this parcel"
		if len(r.Caveats) > 0 {
			reason = r.Caveats[0]
		}
		return nil, reason, false
	}

	r.Findings = append(r.Findings, canopyFindings(r.Canopy, res.RingCroplandPixels)...)
	r.Findings = append(r.Findings, soilFindings(r.Soil)...)

	if res.BaselineYears == 0 {
		r.Caveats = append(r.Caveats,
			"no prior seasons were measured, so this season is compared with "+
				"its neighbours only")
	}
	if res.RingCroplandPixels > 0 && res.RingCroplandPixels < MinRingPixels {
		r.Caveats = append(r.Caveats, fmt.Sprintf(
			"only %d cropland pixels lie within 1.5 km of the parcel, too few "+
				"for the neighbourhood to be a comparison", res.RingCroplandPixels))
	}
	// A measurement near the incident is kept even when it says nothing
	// notable. An index in the middle of the field's range is a fact about the
	// field, and discarding it as "absent" would tell the officer there was no
	// moisture record when there was one that simply showed nothing unusual.
	nearCanopy := r.Canopy != nil && r.Canopy.Near != nil
	nearSoil := r.Soil != nil && r.Soil.Index != nil
	if len(r.Findings) == 0 && !nearCanopy && !nearSoil {
		return nil, "the moisture record had passes, but none near enough the " +
			"incident to compare", false
	}
	return r, "", true
}

// canopy builds the NDMI half, or nil when no usable pass is near the incident.
func canopy(res *satellite.MoistureResult, incident time.Time) *Canopy {
	type yearly struct {
		sum float64
		n   int
	}

	c := &Canopy{}
	var nearSum, beforeSum float64
	var nearN, beforeN int
	var gapSum, gapBeforeSum float64
	var gapN, gapBeforeN int
	prior := map[int]*yearly{}

	for _, p := range res.Optical {
		if !usable(p) {
			continue
		}
		t, err := time.Parse(dayLayout, p.Date)
		if err != nil {
			continue
		}

		// A pass from a prior season is placed on this season's calendar, so
		// "near the incident" means the same days of the year.
		if p.SeasonOffset > 0 {
			t = t.AddDate(p.SeasonOffset, 0, 0)
		}
		days := t.Sub(incident).Hours() / 24

		if p.SeasonOffset == 0 {
			c.Season = append(c.Season, Pass{
				Date: p.Date, NDMI: *p.NDMI, NDVI: p.NDVI,
				RingNDMI: ringValue(p), ClearFraction: p.ClearFraction,
			})
		}

		near := math.Abs(days) <= NearIncidentDays
		before := days < -NearIncidentDays && days >= -BeforeDays

		switch {
		case p.SeasonOffset > 0 && near:
			y := prior[p.SeasonOffset]
			if y == nil {
				y = &yearly{}
				prior[p.SeasonOffset] = y
			}
			y.sum += *p.NDMI
			y.n++
		case p.SeasonOffset == 0 && near:
			nearSum += *p.NDMI
			nearN++
			if ring := ringValue(p); ring != nil {
				gapSum += *p.NDMI - *ring
				gapN++
			}
		case p.SeasonOffset == 0 && before:
			beforeSum += *p.NDMI
			beforeN++
			if ring := ringValue(p); ring != nil {
				gapBeforeSum += *p.NDMI - *ring
				gapBeforeN++
			}
		}
	}

	if nearN == 0 && beforeN == 0 {
		return nil
	}

	c.NearPasses = nearN
	if nearN > 0 {
		c.Near = round(nearSum / float64(nearN))
	}
	if beforeN > 0 {
		c.Before = round(beforeSum / float64(beforeN))
	}
	if gapN > 0 {
		c.NeighbourGap = round(gapSum / float64(gapN))
	}
	if gapBeforeN > 0 {
		c.NeighbourGapBefore = round(gapBeforeSum / float64(gapBeforeN))
	}

	// A mean of per-year means, not of passes. A wet year with six clear
	// passes and a dry year with one would otherwise be a wet normal.
	var means []float64
	for _, y := range prior {
		means = append(means, y.sum/float64(y.n))
	}
	sort.Float64s(means)
	c.PriorYears = len(means)
	if len(means) > 0 {
		m := mean(means)
		c.PriorMean = round(m)
		if len(means) >= 3 {
			c.PriorSpread = round(stddev(means, m))
		}
		if c.Near != nil {
			c.Anomaly = round(*c.Near - m)
		}
	}

	sort.Slice(c.Season, func(i, j int) bool { return c.Season[i].Date < c.Season[j].Date })
	return c
}

func usable(p satellite.OpticalPass) bool {
	return p.NDMI != nil &&
		*p.NDMI >= -1 && *p.NDMI <= 1 &&
		p.ClearFraction >= MinClearFraction &&
		p.ClearPixels >= MinClearPixels
}

func ringValue(p satellite.OpticalPass) *float64 {
	if p.RingNDMI == nil || p.RingClearPixels < MinRingPixels ||
		*p.RingNDMI < -1 || *p.RingNDMI > 1 {
		return nil
	}
	return p.RingNDMI
}

// canopyFindings turns the NDMI comparisons into statements.
func canopyFindings(c *Canopy, ringPixels int) []Finding {
	if c == nil || c.Near == nil {
		return nil
	}

	var out []Finding
	historyDry := c.Anomaly != nil && *c.Anomaly <= -NDMIMaterial
	historyLevel := c.Anomaly != nil && math.Abs(*c.Anomaly) < NDMINoise

	// The gap to the neighbours is read as a change, where the field's gap
	// before the incident is known. A field that has always been a few
	// hundredths drier than the land around it -- lighter soil, an earlier
	// variety -- has not become drier because the gap is negative.
	gap := c.NeighbourGap
	if gap != nil && c.NeighbourGapBefore != nil {
		g := *c.NeighbourGap - *c.NeighbourGapBefore
		gap = &g
	}
	neighbourDry := gap != nil && *gap <= -NDMIMaterial
	neighbourLevel := gap != nil && math.Abs(*gap) < NDMINoise
	neighbourWet := c.NeighbourGap != nil && *c.NeighbourGap >= NDMIMaterial

	var caveats []string
	if c.NearPasses == 1 {
		caveats = append(caveats,
			"one clear Sentinel-2 pass near the incident; a single date can "+
				"carry cloud residue the mask missed")
	}
	if c.PriorYears == 1 {
		caveats = append(caveats,
			"one prior season observed over these days, so the field's normal "+
				"is one year rather than a range")
	}

	switch {
	case historyDry && neighbourDry:
		conf := ConfidenceModerate
		if c.NearPasses >= 2 && c.PriorYears >= 2 {
			conf = ConfidenceHigh
		}
		out = append(out, Finding{
			Code:       FindingCanopyWaterStress,
			Confidence: conf,
			Detail: fmt.Sprintf(
				"leaf water near the incident was %.2f NDMI, %.2f below this "+
					"field's own figure for the same days in %d prior season(s) "+
					"and %.2f below the change in the cropland around it. Drier "+
					"than both its history and its neighbours is the pattern of "+
					"a crop short of water.",
				*c.Near, -*c.Anomaly, c.PriorYears, -*gap),
			Caveats: caveats,
		})

	case historyDry && neighbourLevel:
		out = append(out, Finding{
			Code:       FindingLocalityDry,
			Confidence: confidenceFor(c.NearPasses, c.PriorYears),
			Detail: fmt.Sprintf(
				"leaf water near the incident was %.2f NDMI, %.2f below this "+
					"field's own normal, and the cropland within 1.5 km fell by "+
					"about the same. The whole locality is dry: this corroborates "+
					"the event, and does not by itself distinguish this field.",
				*c.Near, -*c.Anomaly),
			Caveats: caveats,
		})

	case historyDry:
		// Dry against history, and the neighbours cannot be read.
		why := "the neighbourhood could not be read on the same dates"
		if ringPixels > 0 && ringPixels < MinRingPixels {
			why = "there is too little cropland around the parcel to compare"
		}
		out = append(out, Finding{
			Code:       FindingCanopyWaterStress,
			Confidence: ConfidenceLow,
			Detail: fmt.Sprintf(
				"leaf water near the incident was %.2f NDMI, %.2f below this "+
					"field's own normal for these days; %s, so whether the field "+
					"or the locality was dry cannot be told apart.",
				*c.Near, -*c.Anomaly, why),
			Caveats: caveats,
		})

	case neighbourDry && c.Anomaly == nil:
		out = append(out, Finding{
			Code:       FindingCanopyWaterStress,
			Confidence: ConfidenceLow,
			Detail: fmt.Sprintf(
				"leaf water near the incident fell %.2f NDMI further than in the "+
					"cropland around it. No prior season was observed over these "+
					"days, so this rests on the neighbourhood alone.",
				-*gap),
			Caveats: caveats,
		})

	case historyLevel && (neighbourLevel || gap == nil):
		out = append(out, Finding{
			Code:       FindingNoCanopyWaterChange,
			Confidence: confidenceFor(c.NearPasses, c.PriorYears),
			Detail: fmt.Sprintf(
				"leaf water near the incident was %.2f NDMI, within %.2f of this "+
					"field's own normal for these days. Sentinel-2 saw the canopy "+
					"holding its usual water.",
				*c.Near, NDMINoise),
			Caveats: caveats,
		})
	}

	// Independent of the above: markedly wetter than every field around it.
	// In a dry spell the ordinary cause is irrigation, and a drought claim on
	// an irrigated field is worth a second look -- which is all this says.
	if neighbourWet && (c.NeighbourGapBefore == nil || *c.NeighbourGapBefore >= NDMIMaterial) {
		out = append(out, Finding{
			Code:       FindingWetterThanNeighbours,
			Confidence: confidenceFor(c.NearPasses, 2),
			Detail: fmt.Sprintf(
				"the field held %.2f NDMI more leaf water than the cropland within "+
					"1.5 km near the incident. Under the same weather the usual "+
					"reason is irrigation; it is not by itself a reason to doubt "+
					"a claim that is not about water.",
				*c.NeighbourGap),
			Caveats: caveats,
		})
	}

	return out
}

func confidenceFor(passes, years int) string {
	switch {
	case passes >= 2 && years >= 2:
		return ConfidenceModerate
	default:
		return ConfidenceLow
	}
}

// soil builds the Sentinel-1 half on the best-observed relative orbit.
//
// One orbit, not all of them mixed. The same unchanged field reads several
// decibels apart from two viewing geometries, so a dry floor from one and a
// wet ceiling from the other would report a change of orbit as a change of
// moisture.
func soil(passes []satellite.SoilPass, incident time.Time, c *Canopy) *Soil {
	byOrbit := map[int][]satellite.SoilPass{}
	for _, p := range passes {
		if p.RelativeOrbit == nil || p.VVdB < -35 || p.VVdB > 5 {
			continue
		}
		if p.Coverage != nil && *p.Coverage < MinClearFraction {
			continue
		}
		if _, err := time.Parse(dayLayout, p.Date); err != nil {
			continue
		}
		byOrbit[*p.RelativeOrbit] = append(byOrbit[*p.RelativeOrbit], p)
	}

	best, bestN := 0, 0
	for orbit, ps := range byOrbit {
		// Ties broken by orbit number, so the choice does not depend on map
		// iteration order and the same input always yields the same reading.
		if len(ps) > bestN || (len(ps) == bestN && orbit < best) {
			best, bestN = orbit, len(ps)
		}
	}
	if bestN < MinSoilHistory {
		return nil
	}

	series := byOrbit[best]
	sort.Slice(series, func(i, j int) bool { return series[i].Date < series[j].Date })

	values := make([]float64, 0, len(series))
	for _, p := range series {
		values = append(values, p.VVdB)
	}
	sort.Float64s(values)
	// The 5th and 95th percentiles, not the extremes. One pass over a
	// waterlogged field or a freshly ploughed one would otherwise set the
	// ceiling or the floor for two years of readings.
	dry, wet := percentile(values, 0.05), percentile(values, 0.95)
	if wet-dry < 1.0 {
		// A field whose VV never moved more than a decibel in two years is
		// not showing moisture at all -- it is showing a permanent canopy, a
		// roof, or a scene that was never recalibrated.
		return nil
	}

	s := &Soil{
		RelativeOrbit: best,
		DryDB:         round2(dry),
		WetDB:         round2(wet),
		HistoryPasses: len(series),
	}

	var nearest *satellite.SoilPass
	nearestDays := math.MaxFloat64
	var before *satellite.SoilPass
	for i := range series {
		p := &series[i]
		t, _ := time.Parse(dayLayout, p.Date)
		days := t.Sub(incident).Hours() / 24
		if math.Abs(days) <= NearIncidentDays && math.Abs(days) < nearestDays {
			nearest, nearestDays = p, math.Abs(days)
		}
		if days < -NearIncidentDays && days >= -BeforeDays {
			before = p
		}
	}
	if nearest == nil {
		return nil
	}

	s.Index = round(clamp01((nearest.VVdB - dry) / (wet - dry)))
	s.IndexDate = nearest.Date
	if before != nil {
		s.Before = round(clamp01((before.VVdB - dry) / (wet - dry)))
		s.BeforeDate = before.Date
	}

	// Dense canopy on the nearest optical pass means radar saw leaves.
	if c != nil {
		for _, p := range c.Season {
			if p.NDVI != nil && *p.NDVI >= DenseCanopyNDVI {
				t, _ := time.Parse(dayLayout, p.Date)
				if math.Abs(t.Sub(incident).Hours()/24) <= NearIncidentDays {
					s.CanopyObscured = true
					break
				}
			}
		}
	}
	return s
}

func soilFindings(s *Soil) []Finding {
	if s == nil || s.Index == nil {
		return nil
	}

	var caveats []string
	conf := ConfidenceModerate
	if s.CanopyObscured {
		conf = ConfidenceLow
		caveats = append(caveats,
			"the crop was dense near the incident, so radar mostly saw the "+
				"canopy and this index is a weak guide to the soil beneath it")
	}
	caveats = append(caveats, fmt.Sprintf(
		"relative to this field's own range on relative orbit %d over %d passes; "+
			"it is not a volumetric water content", s.RelativeOrbit, s.HistoryPasses))

	switch {
	case *s.Index >= SoilWet && (s.Before == nil || *s.Before < SoilWet):
		return []Finding{{
			Code:       FindingSurfaceWet,
			Confidence: conf,
			Detail: fmt.Sprintf(
				"on %s the soil read %.0f%% of the way from this field's driest to "+
					"its wettest, which is near the top of what it reaches. "+
					"Consistent with heavy rain or standing water.",
				s.IndexDate, *s.Index*100),
			Caveats: caveats,
		}}
	case *s.Index <= SoilDry:
		return []Finding{{
			Code:       FindingSurfaceDry,
			Confidence: conf,
			Detail: fmt.Sprintf(
				"on %s the soil read %.0f%% of the way from this field's driest to "+
					"its wettest, near the bottom of its range. Consistent with a "+
					"dry spell.",
				s.IndexDate, *s.Index*100),
			Caveats: caveats,
		}}
	}
	return nil
}

// Relevance is how much moisture evidence bears on a calamity, in words.
func Relevance(calamity string) string {
	switch strings.ToLower(strings.TrimSpace(calamity)) {
	case "drought":
		return "Strong. Leaf water falls before greenness does, so NDMI is " +
			"among the earliest satellite evidence of a crop short of water."
	case "flood", "cloudburst", "unseasonal_rain", "cyclone":
		return "Limited for leaf water, which cloud usually hides during the " +
			"event; the Sentinel-1 soil index, which sees through cloud, is the " +
			"part that bears on excess water."
	case "hailstorm", "fire", "landslide":
		return "Weak. This calamity breaks or removes the crop, and a damaged " +
			"plant keeps its leaf water for some time; radar and the ground " +
			"survey bear on it far more directly."
	case "pest_attack", "frost":
		return "Modest. Stress of any kind can lower leaf water, and this " +
			"evidence cannot say which kind it was."
	default:
		return "Corroborating only. It says how wet the canopy and the soil " +
			"were, not what caused a loss."
	}
}

// --- arithmetic ------------------------------------------------------------

func round(v float64) *float64 {
	r := math.Round(v*1000) / 1000
	return &r
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

func mean(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

func stddev(xs []float64, m float64) float64 {
	var s float64
	for _, x := range xs {
		s += (x - m) * (x - m)
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

// percentile over a sorted slice, by linear interpolation.
func percentile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

func clamp01(v float64) float64 {
	return math.Max(0, math.Min(1, v))
}
