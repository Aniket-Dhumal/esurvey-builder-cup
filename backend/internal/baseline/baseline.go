// Package baseline answers "sixty per cent of what".
//
// # The gap this closes
//
// A farmer declares a percentage, an officer records one, and the panel is
// asked whether it is plausible. Nowhere did this system hold what the crop
// would have yielded had nothing happened, so the figure read as a
// measurement while being a judgement.
//
// Two denominators are derivable from imagery this platform already buys.
//
// What this land normally does, from its own prior seasons. A parcel that
// peaked at 0.74 NDVI this year against 0.71, 0.76 and 0.69 before has
// suffered a real loss when it falls to 0.31. The same fall from 0.74 against
// a history of 0.45 is a return to normal, and possibly a claim staked on an
// unusually good year.
//
// What the surrounding fields did in the same fortnight. One parcel's fall is
// ambiguous between a regional calamity, a local one, an early harvest and a
// false claim. The same fall measured against neighbours that did not fall
// resolves all four, and it is the strongest fraud signal available that
// needs no photograph and no officer visit.
//
// # Why the cache is part of the design and not an optimisation
//
// Each baseline is another billable analysis. Three prior seasons per parcel
// per assessment multiplies the cost of every claim by four, to re-derive an
// answer that cannot have changed. Two facts make it cheap instead: a closed
// window over an immutable archive has a permanent answer, and a
// neighbourhood is shared by everyone farming in it. In the village-wide
// hailstorm this platform exists for, the second is the difference between
// one analysis and four hundred.
//
// # What this never does
//
// It never fails an assessment. Every baseline is evidence that may be
// absent, and an assessment made without one is worse but still an
// assessment. Failures come back as stated reasons, in words an officer can
// read, and the panel is told what it did not get.
package baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/platform/db"
	"github.com/agribid/esurvey-platform/backend/internal/rscache"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
)

// Engine is the part of the analytics client this package needs.
//
// An interface rather than the concrete client so the cost and correctness
// behaviour -- how many analyses are requested, and what happens when one
// fails -- can be tested without an engine and without spending anything.
type Engine interface {
	NDVI(ctx context.Context, req satellite.NDVIRequest) (*satellite.NDVIResult, error)
}

// Store computes and caches baselines.
type Store struct {
	pool  *db.Pool
	cache *rscache.Store
	eng   Engine
	log   *slog.Logger

	// PriorSeasons is how far back the parcel's own history is read.
	//
	// Three is the default deliberately. Two gives a comparison but no spread
	// to judge it against; beyond five the parcel may have changed hands,
	// crop or irrigation, and an older season is answering a question about a
	// different field.
	PriorSeasons int

	// NeighbourhoodRadiusM is the half-width of the peer window.
	NeighbourhoodRadiusM int
}

func NewStore(pool *db.Pool, eng Engine, log *slog.Logger) *Store {
	return &Store{
		pool: pool, cache: rscache.New(pool, log), eng: eng, log: log,
		PriorSeasons:         3,
		NeighbourhoodRadiusM: 3000,
	}
}

// ---------------------------------------------------------------------------
// A1 -- what this land normally does
// ---------------------------------------------------------------------------

// SeasonPeak is one prior season's best.
type SeasonPeak struct {
	// Year is the calendar year the window started in.
	Year         int     `json:"year"`
	WindowStart  string  `json:"window_start"`
	WindowEnd    string  `json:"window_end"`
	PeakNDVI     float64 `json:"peak_ndvi"`
	PeakDate     string  `json:"peak_date"`
	Observations int     `json:"observations"`
}

// History is the parcel measured against itself.
type History struct {
	ThisSeasonPeak float64      `json:"this_season_peak"`
	Seasons        []SeasonPeak `json:"prior_seasons"`

	MeanPeak float64 `json:"mean_prior_peak,omitempty"`
	StdDev   float64 `json:"stddev_prior_peak,omitempty"`

	// ZScore is how unusual this season's peak is against the parcel's own
	// spread. Nil when too few seasons came back to mean anything -- which is
	// the honest answer, not zero.
	ZScore *float64 `json:"z_score,omitempty"`

	// Verdict is what the history says, in one word the panel can act on.
	Verdict string `json:"verdict"`

	Confidence string   `json:"confidence"`
	Caveats    []string `json:"caveats,omitempty"`
}

// Verdicts for the parcel's own history.
const (
	// VerdictBelowNormal means this season's canopy never reached what this
	// land usually manages -- consistent with a loss, or with a poor season
	// that preceded the calamity.
	VerdictBelowNormal = "below_normal"
	VerdictNormal      = "normal"
	// VerdictAboveNormal is worth stating plainly: a claim against a season
	// that grew better than usual is not thereby false, but the denominator
	// is larger than the farmer's memory of an average year.
	VerdictAboveNormal = "above_normal"
	VerdictUnknown     = "unknown"
)

// zNotable is the departure from the parcel's own mean that counts as
// something rather than noise. One standard deviation, stated rather than
// tuned, so a reader can disagree with it.
const zNotable = 1.0

// ParcelHistory reads the parcel's own prior seasons.
//
// The same calendar window is used each year rather than a fixed number of
// days back, because the question is what this land does at this point in its
// cycle, and a monsoon does not shift by leap days.
func (s *Store) ParcelHistory(
	ctx context.Context, geom satellite.Geometry,
	farmID, sowingDate, incidentDate string, thisSeasonPeak float64,
) (*History, error) {
	start, err := time.Parse(dayLayout, sowingDate)
	if err != nil {
		return nil, fmt.Errorf("sowing date %q: %w", sowingDate, err)
	}
	end, err := time.Parse(dayLayout, incidentDate)
	if err != nil {
		return nil, fmt.Errorf("incident date %q: %w", incidentDate, err)
	}

	h := &History{ThisSeasonPeak: round3(thisSeasonPeak), Verdict: VerdictUnknown}
	key := geometryKey(geom)

	for back := 1; back <= s.PriorSeasons; back++ {
		ws := start.AddDate(-back, 0, 0)
		we := end.AddDate(-back, 0, 0)

		res, err := s.ndvi(ctx, "ndvi_parcel", key, satellite.NDVIRequest{
			Geometry:     geom,
			SowingDate:   ws.Format(dayLayout),
			IncidentDate: we.Format(dayLayout),
			FieldID:      farmID,
		})
		if err != nil {
			// One missing season is not a failure. The engine refuses windows
			// before the archive starts, and a parcel may simply not have
			// been cropped that year.
			h.Caveats = append(h.Caveats, fmt.Sprintf(
				"the %d season could not be read: %s", ws.Year(), err))
			continue
		}

		peak, date, n := peakOf(res.TimeSeries)
		if n < 2 {
			h.Caveats = append(h.Caveats, fmt.Sprintf(
				"the %d season had %d cloud-free passes, too few to establish "+
					"what this land does", ws.Year(), n))
			continue
		}

		h.Seasons = append(h.Seasons, SeasonPeak{
			Year:         ws.Year(),
			WindowStart:  ws.Format(dayLayout),
			WindowEnd:    we.Format(dayLayout),
			PeakNDVI:     round3(peak),
			PeakDate:     date,
			Observations: n,
		})
	}

	switch len(h.Seasons) {
	case 0:
		h.Confidence = confidenceLow
		h.Caveats = append(h.Caveats,
			"no prior season could be read for this parcel, so this year's "+
				"canopy is not measured against anything")
		return h, nil
	case 1, 2:
		h.Confidence = confidenceLow
	case 3:
		h.Confidence = confidenceModerate
	default:
		h.Confidence = confidenceHigh
	}

	var sum float64
	for _, p := range h.Seasons {
		sum += p.PeakNDVI
	}
	h.MeanPeak = round3(sum / float64(len(h.Seasons)))

	if len(h.Seasons) >= 2 {
		var sq float64
		for _, p := range h.Seasons {
			d := p.PeakNDVI - h.MeanPeak
			sq += d * d
		}
		// Sample standard deviation: these are a sample of the seasons this
		// parcel has had, not the population of them.
		h.StdDev = round3(math.Sqrt(sq / float64(len(h.Seasons)-1)))
	}

	switch {
	case len(h.Seasons) < 2:
		h.Caveats = append(h.Caveats,
			"one prior season is a comparison, not a spread; the difference "+
				"below cannot be called unusual or ordinary")
		if thisSeasonPeak < h.MeanPeak-0.1 {
			h.Verdict = VerdictBelowNormal
		} else {
			h.Verdict = VerdictNormal
		}
	case h.StdDev < 0.02:
		// A parcel with no variation between seasons makes any z enormous.
		// Falling back to an absolute difference is the honest reading.
		h.Caveats = append(h.Caveats,
			"prior seasons varied almost not at all, so a standardised score "+
				"would exaggerate any difference; an absolute comparison was used")
		switch {
		case thisSeasonPeak < h.MeanPeak-0.1:
			h.Verdict = VerdictBelowNormal
		case thisSeasonPeak > h.MeanPeak+0.1:
			h.Verdict = VerdictAboveNormal
		default:
			h.Verdict = VerdictNormal
		}
	default:
		z := round3((thisSeasonPeak - h.MeanPeak) / h.StdDev)
		h.ZScore = &z
		switch {
		case z <= -zNotable:
			h.Verdict = VerdictBelowNormal
		case z >= zNotable:
			h.Verdict = VerdictAboveNormal
		default:
			h.Verdict = VerdictNormal
		}
	}

	if h.Verdict == VerdictAboveNormal {
		h.Caveats = append(h.Caveats,
			"this season's canopy was better than this parcel usually manages; "+
				"a loss from that peak is real, but it is a fall from an unusually "+
				"good year rather than from an average one")
	}

	return h, nil
}

// ---------------------------------------------------------------------------
// A2 -- what the neighbours did
// ---------------------------------------------------------------------------

// Peer is the parcel measured against the ground around it.
type Peer struct {
	GridCell string `json:"grid_cell"`
	RadiusM  int    `json:"radius_m"`

	NeighbourhoodPeak     float64 `json:"neighbourhood_peak_ndvi"`
	NeighbourhoodIncident float64 `json:"neighbourhood_incident_ndvi"`
	NeighbourhoodRRVL     float64 `json:"neighbourhood_rrvl_percent"`
	Observations          int     `json:"observations"`

	ParcelRRVL float64 `json:"parcel_rrvl_percent"`

	// RelativeAnomaly is the parcel's fall minus the neighbourhood's, in
	// percentage points. Positive means this field fared worse than the
	// ground around it.
	RelativeAnomaly float64 `json:"relative_anomaly_points"`

	Verdict string   `json:"verdict"`
	Reason  string   `json:"reason"`
	Caveats []string `json:"caveats,omitempty"`
}

// What the comparison says.
const (
	// PeerRegional means the parcel and its neighbours both fell. The claimed
	// calamity is corroborated by ground the claimant does not control.
	PeerRegional = "regional_event"
	// PeerLocal means this parcel fell and its neighbours did not. That is
	// consistent with a genuine localised loss, and equally with an early
	// harvest or a misreported one; it is a reason to look, not a finding.
	PeerLocal = "local_only"
	// PeerContradicted means the neighbours fell and this parcel did not.
	PeerContradicted = "not_visible_on_this_parcel"
	PeerNoLoss       = "no_loss_visible_anywhere"
	PeerInconclusive = "inconclusive"
)

// materialRRVL is the fall that counts as a loss being visible at all.
const materialRRVL = 20.0

// Neighbourhood compares the parcel against the ground around it.
//
// The window is a square centred on a snapped grid cell rather than on the
// parcel, so that every claim from the same locality answers from one
// analysis. It is a spatial mean over everything in the square -- other
// people's fields, field boundaries, a road, a tank -- which is why it is
// used as a direction of travel and never as a number to subtract.
func (s *Store) Neighbourhood(
	ctx context.Context, lat, lon float64,
	sowingDate, incidentDate string, parcelRRVL float64,
) (*Peer, error) {
	cell, geom := neighbourhood(lat, lon, s.NeighbourhoodRadiusM)

	res, err := s.ndvi(ctx, "ndvi_neighbourhood", cell, satellite.NDVIRequest{
		Geometry:     geom,
		SowingDate:   sowingDate,
		IncidentDate: incidentDate,
	})
	if err != nil {
		return nil, fmt.Errorf("reading the neighbourhood: %w", err)
	}

	peak, _, n := peakOf(res.TimeSeries)
	p := &Peer{
		GridCell:          cell,
		RadiusM:           s.NeighbourhoodRadiusM,
		NeighbourhoodPeak: round3(peak),
		Observations:      n,
		ParcelRRVL:        round1(parcelRRVL),
		Verdict:           PeerInconclusive,
	}

	if n < 2 {
		p.Reason = fmt.Sprintf(
			"the surrounding area had %d cloud-free passes in this window, which "+
				"is not enough to say what it did", n)
		return p, nil
	}

	last := res.TimeSeries[len(res.TimeSeries)-1]
	p.NeighbourhoodIncident = round3(last.NDVI)
	p.NeighbourhoodRRVL = round1(rrvl(peak, last.NDVI))
	p.RelativeAnomaly = round1(p.ParcelRRVL - p.NeighbourhoodRRVL)

	parcelFell := p.ParcelRRVL >= materialRRVL
	areaFell := p.NeighbourhoodRRVL >= materialRRVL

	switch {
	case parcelFell && areaFell:
		p.Verdict = PeerRegional
		p.Reason = fmt.Sprintf(
			"the surrounding area fell %.0f%% over the same window, against this "+
				"parcel's %.0f%%. The claimed event is visible on ground the "+
				"claimant does not control, which corroborates it.",
			p.NeighbourhoodRRVL, p.ParcelRRVL)
	case parcelFell && !areaFell:
		p.Verdict = PeerLocal
		p.Reason = fmt.Sprintf(
			"this parcel fell %.0f%% while the surrounding area fell %.0f%%. A "+
				"genuinely localised loss looks exactly like this, and so does an "+
				"early harvest or a misreported date; it is a reason to look, not "+
				"a finding.",
			p.ParcelRRVL, p.NeighbourhoodRRVL)
	case !parcelFell && areaFell:
		p.Verdict = PeerContradicted
		p.Reason = fmt.Sprintf(
			"the surrounding area fell %.0f%% but this parcel fell only %.0f%%. "+
				"The event appears real and this field appears to have come "+
				"through it.",
			p.NeighbourhoodRRVL, p.ParcelRRVL)
	default:
		p.Verdict = PeerNoLoss
		p.Reason = fmt.Sprintf(
			"neither this parcel (%.0f%%) nor the surrounding area (%.0f%%) shows "+
				"a material fall in canopy over this window.",
			p.ParcelRRVL, p.NeighbourhoodRRVL)
	}

	p.Caveats = append(p.Caveats,
		"the surrounding figure is a spatial mean over everything inside a "+
			"square kilometre grid -- other fields, boundaries, roads and water -- "+
			"so it indicates direction, not a quantity to subtract")

	return p, nil
}

const (
	confidenceHigh     = "high"
	confidenceModerate = "moderate"
	confidenceLow      = "low"
	dayLayout          = "2006-01-02"
)

func peakOf(ts []satellite.NDVIPoint) (peak float64, date string, n int) {
	for _, p := range ts {
		if p.NDVI > peak {
			peak, date = p.NDVI, p.Date
		}
	}
	return peak, date, len(ts)
}

// rrvl is the relative reduction in vegetation level, as a percentage of the
// baseline. Kept identical in meaning to the figure the assessment package
// already reports, so the two cannot be read as different measures.
func rrvl(baseline, incident float64) float64 {
	if baseline <= 0 {
		return 0
	}
	return math.Max(0, (baseline-incident)/baseline*100)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func geometryKey(g satellite.Geometry) string {
	sum := sha256.Sum256(append([]byte(g.Type), g.Coordinates...))
	return hex.EncodeToString(sum[:16])
}

// gridDegrees is how coarsely a neighbourhood is snapped.
//
// Two hundredths of a degree is about 2.2 km. Coarser would put parcels with
// genuinely different weather in one cell; finer would stop neighbours
// sharing a result, which is the entire cost argument.
const gridDegrees = 0.02

// neighbourhood builds the square a locality is read over, and the key it is
// cached under.
func neighbourhood(lat, lon float64, radiusM int) (string, satellite.Geometry) {
	snapLat := math.Round(lat/gridDegrees) * gridDegrees
	snapLon := math.Round(lon/gridDegrees) * gridDegrees

	// A degree of latitude is close enough to 111 km everywhere. A degree of
	// longitude shrinks with the cosine, and ignoring that would make a
	// neighbourhood in Nagpur a different size from one in Sindhudurg.
	dLat := float64(radiusM) / 111_000
	cos := math.Cos(snapLat * math.Pi / 180)
	if cos < 0.1 {
		cos = 0.1
	}
	dLon := float64(radiusM) / (111_000 * cos)

	w, e := snapLon-dLon, snapLon+dLon
	sLat, nLat := snapLat-dLat, snapLat+dLat

	ring := [][][2]float64{{
		{w, sLat}, {e, sLat}, {e, nLat}, {w, nLat}, {w, sLat},
	}}
	coords, _ := json.Marshal(ring)

	key := fmt.Sprintf("%.2f,%.2f,%d", snapLat, snapLon, radiusM)
	return key, satellite.Geometry{Type: "Polygon", Coordinates: coords}
}
