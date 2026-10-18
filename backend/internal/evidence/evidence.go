// Package evidence assembles everything an assessment can stand on.
//
// # Why one place
//
// Six sources now feed a damage figure: the parcel's own prior seasons, the
// fields around it, radar that sees through cloud, the season's weather, the
// crop's position in its own cycle, what an officer counted, and what has
// been claimed here before. Left in the handler they would be six blocks of
// error handling in a function that is already long, and the rule that
// matters -- that no single one of them may fail an assessment -- would be
// six separate decisions to get right.
//
// # The rule
//
// Nothing here fails an assessment. Every source is evidence that may be
// absent, and an assessment made without one is worse but still an
// assessment. What is missing is named, in words an officer can read, and the
// panel is told what it did not get rather than left to infer it from silence.
//
// That matters more than it sounds. A packet with everything absent and a
// packet with everything present look identical to a model that is only shown
// what arrived, and the second is a far better assessment than the first.
//
// # Why they run together
//
// They are independent network calls of several seconds each. Run in sequence
// they would add most of a minute to every assessment; run together they cost
// the slowest one. The failure of any is contained to that source.
package evidence

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agronomy"
	"github.com/agribid/esurvey-platform/backend/internal/ai"
	"github.com/agribid/esurvey-platform/backend/internal/baseline"
	"github.com/agribid/esurvey-platform/backend/internal/claims"
	"github.com/agribid/esurvey-platform/backend/internal/fieldwork"
	"github.com/agribid/esurvey-platform/backend/internal/groundcv"
	"github.com/agribid/esurvey-platform/backend/internal/moisture"
	"github.com/agribid/esurvey-platform/backend/internal/platform/saferr"
	"github.com/agribid/esurvey-platform/backend/internal/pointndvi"
	"github.com/agribid/esurvey-platform/backend/internal/sar"
	"github.com/agribid/esurvey-platform/backend/internal/satellite"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
	"github.com/agribid/esurvey-platform/backend/internal/vision"
	"github.com/agribid/esurvey-platform/backend/internal/weather"
)

// Radar is the part of the analytics client this package needs.
type Radar interface {
	SAR(ctx context.Context, req satellite.SARRequest) (*satellite.SARResult, error)
}

// GroundVision measures the damage visible in a farmer's survey video.
//
// The signal the specification weights above every other -- 0.50 of the
// composite in clear weather and 0.65 once cloud has taken the optical record
// away -- and the only one that looks at the thing actually being claimed.
//
// An interface, and nil in every deployment today, because no vision pipeline
// exists yet. That absence is the point of declaring it: an assessment now
// says "no ground vision pipeline is configured" in its Absent list, where
// before the platform simply never mentioned that the heaviest input in its
// own specification was missing.
type GroundVision interface {
	// Measure returns one reading per directed point: the walk is four
	// points because corners differ, and a single reading throws that away.
	//
	// cropName is what the farmer declared, where it is known. The model
	// reads the same footage differently for a ripening cereal and a
	// broadleaf, and an empty crop is handled rather than refused.
	Measure(ctx context.Context, reportID, cropName string) ([]vision.Clip, error)
}

// PointOptical reads the optical record at the spots the farmer filmed.
//
// A second reading from the same engine over the same window, on a small patch
// around each point of the walk rather than on the whole parcel. Declared as an
// interface for the same reason GroundVision is: a deployment without it says
// so in the Absent list, rather than leaving an officer to assume the check was
// made.
//
// Additive. Nothing it returns changes the parcel-wide series, the peak, the
// RRVL or the loss figure derived from them.
type PointOptical interface {
	// SpotSeries returns one series per recorded point, keyed by point number,
	// for the ground inside the parcel within RadiusM of where each recording
	// was made. Points it could not sample are simply absent from the map,
	// with the reason in the error only if nothing could be sampled at all.
	SpotSeries(ctx context.Context, req PointRequest) (map[int]PointSample, error)
}

// OfficialRainfall is the state's own rainfall record for an administrative
// unit: what fell, against the normal for that place.
//
// Kept behind an interface because it is the one source here whose absence is
// entirely ordinary. Nobody has run the loader for most districts, and an
// assessment in a district with no stored rainfall must be no worse off than it
// was before this source existed -- it reports the source absent and falls back
// on the reanalysis for everything, as it always did.
type OfficialRainfall interface {
	// ForVillage returns the published rainfall of the unit containing a
	// village, or nil where none has been loaded. Nil is not an error.
	ForVillage(ctx context.Context, villageLGDCode string, cropYear int) (
		*weather.Official, error)
}

// PointRequest is which walk to sample, over which window.
//
// The window is the parcel's own, deliberately: two readings taken over
// different windows are not comparable, and the comparison is the entire point
// of taking the second one.
type PointRequest struct {
	ReportID     string
	FarmID       string
	FarmerID     string
	SowingDate   string
	IncidentDate string
	RadiusM      float64
}

// PointSample is one point's patch and what the engine saw over it.
type PointSample struct {
	// AreaM2 is how much parcel survived clipping the buffer to the boundary.
	AreaM2 float64

	// OutsideParcel is true where the recording itself was made outside.
	OutsideParcel bool

	Series []satellite.NDVIPoint
}

// Assembler gathers evidence. Every field is optional: a deployment without a
// database, without radar or without weather assembles what it can.
type Assembler struct {
	Baselines *baseline.Store
	Weather   *weather.Client
	Claims    *claims.Store
	Fieldwork *fieldwork.Store
	Radar     Radar
	Moisture  Moisture
	Vision    GroundVision
	Points    PointOptical
	Official  OfficialRainfall
	Log       *slog.Logger

	// AllowSynthetic permits a generated radar series or moisture record to be
	// used as evidence.
	//
	// Development only, and off by default, so a deployment that forgets to
	// set it refuses generated readings rather than quietly believing them.
	// The zero value of a bool is the safe answer here, which is the whole
	// reason the flag is phrased as a permission rather than as a block.
	AllowSynthetic bool

	// Budget bounds the whole assembly. Past it, whatever has arrived is
	// used and the rest is reported absent -- an assessment that is late is
	// worse than one that is thin, because the officer is waiting.
	Budget time.Duration
}

// Input is what identifies the claim being assessed.
type Input struct {
	ReportID     string
	FarmerID     string
	FarmID       string
	SurveyNumber string
	VillageCode  string
	EventID      string

	CropName        string
	VarietyDuration string
	Season          string
	CropYear        string
	CalamityType    string

	SowingDate   string
	IncidentDate string

	// DeclaredWindow is the incident period of the survey event this
	// assessment belongs to, where it belongs to one. When set it is the
	// window the weather is judged over, in place of the fortnight before the
	// reported date -- a district declaring drought over three weeks is
	// naming the period to examine, and the farmer's own date is only when
	// they noticed.
	DeclaredWindow weather.Span

	Geometry satellite.Geometry
	Lat, Lon float64
	HasPoint bool

	// PeakNDVI and RRVL come from the optical analysis that has already run,
	// so the baselines compare like with like rather than recomputing them.
	PeakNDVI float64
	RRVL     float64

	// OpticalGapFrom and OpticalGapTo bound the longest stretch the optical
	// series could not see, so radar can be asked whether anything watched it.
	OpticalGapFrom string
	OpticalGapTo   string

	DeclaredPercent float64
	FieldAreaHa     float64
}

// Packet is everything that could be gathered, and everything that could not.
type Packet struct {
	Denominators *baseline.Denominators `json:"denominators,omitempty"`

	Weather *weather.Record `json:"weather,omitempty"`

	// OfficialRainfall is the state's published figure, and where it is present
	// it is the authoritative amount: the reanalysis above it is a model, this
	// is a network of gauges, and only this one carries the normal that a
	// deficit is measured against.
	OfficialRainfall *weather.Official `json:"official_rainfall,omitempty"`

	Thermal        *agronomy.Thermal        `json:"thermal,omitempty"`
	Stress         *agronomy.Stress         `json:"stress,omitempty"`
	Susceptibility *agronomy.Susceptibility `json:"susceptibility,omitempty"`
	SowingCheck    *agronomy.SowingCheck    `json:"sowing_check,omitempty"`

	Radar          *sar.Reading `json:"radar,omitempty"`
	RadarCoversGap int          `json:"radar_passes_in_optical_gap,omitempty"`

	// Moisture is leaf water from Sentinel-2 NDMI and surface soil moisture
	// from the Sentinel-1 record, each against this field's own prior seasons
	// and the cropland around it. Corroborating only: it never enters a
	// damage figure, and it says how much it bears on the calamity claimed.
	Moisture *moisture.Reading `json:"moisture,omitempty"`

	// GroundCV is what the survey video measured. The heaviest single input
	// in the specification's fusion, and the only evidence here that sees the
	// crop rather than the field it stands in.
	GroundCV *groundcv.Score `json:"ground_cv,omitempty"`

	// PointNDVI is the optical record at the spots the footage came from,
	// against the parcel's own. Added to catch what a field mean cannot: a
	// clip of a ruined crop taken where the satellite shows vigour, and a walk
	// that sampled only the worst ground. Nil where the walk has no recorded
	// points, or where the sampling could not be run.
	PointNDVI *pointndvi.Comparison `json:"point_ndvi,omitempty"`

	// GroundCrop is whether the field holds the crop that was claimed.
	//
	// Kept beside the score rather than inside it because it is a different
	// kind of statement: the score is about a crop's condition, this is about
	// its identity. Nothing else in this packet can answer it -- the registry
	// records what was sown months ago and the satellite cannot tell soybean
	// from sugarcane on two hectares -- and a claim filed for a crop that is
	// not in the field is the fraud the walk is best placed to catch.
	GroundCrop *vision.CropCheck `json:"ground_crop,omitempty"`

	History *claims.History `json:"claim_history,omitempty"`

	Measurements *fieldwork.Summary `json:"field_measurements,omitempty"`

	// Absent names each source that could not be obtained and why. The field
	// an officer should read first: an assessment with most of this list
	// populated is a judgement, and should be read as one.
	Absent []string `json:"absent,omitempty"`

	// Elapsed is how long the assembly took, so a slow source is visible
	// before it becomes a complaint about the platform being slow.
	Elapsed string `json:"elapsed,omitempty"`
}

// defaultBudget is the whole assembly's ceiling.
const defaultBudget = 90 * time.Second

// Assemble gathers what it can within the budget.
//
// Never returns an error. A packet that gathered nothing is a valid packet
// whose Absent list explains itself, and failing here would take down an
// assessment that the panel can still make from ground photographs.
func (a *Assembler) Assemble(ctx context.Context, in Input, series []agronomy.Observation) *Packet {
	started := time.Now()

	budget := a.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	p := &Packet{}
	var mu sync.Mutex
	// Every source that could not be gathered, and why, in words an officer
	// can act on.
	//
	// This list is read carefully -- it sits under "Could not be gathered" on
	// the assessment screen, and an officer deciding whether a claim is short
	// of evidence weighs it. So a library's message must not land in it. One
	// did: the weather archive answered 502 with an HTML page, and the officer
	// was shown "decoding the weather archive (http 502): invalid character
	// '<' looking for beginning of value".
	//
	// The full text still reaches the log, because that sentence is exactly
	// what somebody debugging the archive integration needs.
	absent := func(what, why string) {
		if saferr.LooksInternal(why) {
			a.logf("evidence source failed",
				"source", what, "error", why)
			why = "it could not be reached; the assessment was made without it"
		}
		mu.Lock()
		defer mu.Unlock()
		p.Absent = append(p.Absent, what+": "+why)
	}

	crop, cropKnown := agronomy.Find(in.CropName)
	if !cropKnown && in.CropName != "" {
		absent("crop model", fmt.Sprintf(
			"%q is not in the agronomic table, so no growth stage or "+
				"susceptibility could be established for it", in.CropName))
	}

	var wg sync.WaitGroup
	run := func(name string, f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				// A panic in one source must not take the assessment with it.
				if r := recover(); r != nil {
					a.logf("evidence source panicked", "source", name, "panic", r)
					absent(name, "an internal error prevented it")
				}
			}()
			if err := f(); err != nil {
				absent(name, err.Error())
			}
		}()
	}

	// --- the denominators -------------------------------------------------
	run("parcel history", func() error {
		if a.Baselines == nil {
			return errors.New("no baseline store is configured")
		}
		if in.PeakNDVI <= 0 {
			return errors.New("this season has no peak NDVI to compare against")
		}
		h, err := a.Baselines.ParcelHistory(ctx, in.Geometry, in.FarmID,
			in.SowingDate, in.IncidentDate, in.PeakNDVI)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		p.ensureDenominators().History = h
		return nil
	})

	run("neighbourhood", func() error {
		if a.Baselines == nil {
			return errors.New("no baseline store is configured")
		}
		if !in.HasPoint {
			return errors.New("the parcel has no position, so the surrounding " +
				"fields cannot be located")
		}
		peer, err := a.Baselines.Neighbourhood(ctx, in.Lat, in.Lon,
			in.SowingDate, in.IncidentDate, in.RRVL)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		p.ensureDenominators().Peer = peer
		return nil
	})

	run("threshold yield", func() error {
		if a.Baselines == nil {
			return errors.New("no baseline store is configured")
		}
		y, found, err := a.Baselines.ThresholdYieldFor(ctx,
			in.CropName, in.VillageCode, in.Season, in.CropYear)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf(
				"no threshold yield has been issued for %s in this unit for %s, "+
					"so the percentage is not a shortfall against a statutory figure",
				in.CropName, in.CropYear)
		}
		mu.Lock()
		defer mu.Unlock()
		p.ensureDenominators().Yield = y
		return nil
	})

	// --- rainfall as the state recorded it --------------------------------
	//
	// A separate source from the reanalysis below, and where present the
	// authoritative one for how much rain fell. It runs alongside rather than
	// inside the weather step because neither should be able to fail the other:
	// a district nobody has loaded must still get its reanalysis, and a portal
	// that has changed its markup must not cost an assessment its dry spells.
	run("official rainfall", func() error {
		if a.Official == nil {
			return errors.New(
				"no published rainfall source is configured, so the season's " +
					"rain is known only from reanalysis and cannot be compared " +
					"with the normal for this place")
		}
		if in.VillageCode == "" {
			return errors.New("the parcel has no village code, so the taluka " +
				"whose rainfall was published cannot be identified")
		}

		year, err := strconv.Atoi(cropYearStart(in.CropYear, in.SowingDate))
		if err != nil {
			return fmt.Errorf("the crop year %q cannot be read: %w",
				in.CropYear, err)
		}

		off, err := a.Official.ForVillage(ctx, in.VillageCode, year)
		if err != nil {
			return err
		}
		if off == nil {
			return fmt.Errorf(
				"no published rainfall has been loaded for this taluka for %d; "+
					"run rainfall-load for the district", year)
		}

		mu.Lock()
		p.OfficialRainfall = off
		mu.Unlock()
		return nil
	})

	// --- weather, and everything derived from it --------------------------
	run("weather", func() error {
		if a.Weather == nil {
			return errors.New("no weather client is configured")
		}
		if !in.HasPoint {
			return errors.New("the parcel has no position, so the weather over " +
				"it cannot be looked up")
		}
		// Fetch to the end of the declared window where it runs past the
		// reported date, or the window the drought is judged over is a window
		// the series does not hold.
		until := in.IncidentDate
		if in.DeclaredWindow.Valid() && in.DeclaredWindow.To > until {
			until = in.DeclaredWindow.To
		}
		s, err := a.Weather.Range(ctx, in.Lat, in.Lon, in.SowingDate, until)
		if err != nil {
			return err
		}
		rec, err := s.Assemble(in.SowingDate, in.IncidentDate, in.DeclaredWindow)
		if err != nil {
			return err
		}

		mu.Lock()
		p.Weather = rec
		mu.Unlock()

		if !cropKnown {
			return nil
		}
		days := s.Days2Agronomy()

		th, err := agronomy.StageOn(crop, in.VarietyDuration, days,
			in.SowingDate, in.IncidentDate)
		if err != nil {
			absent("growth stage", err.Error())
		} else {
			mu.Lock()
			p.Thermal = &th
			mu.Unlock()

			if s, ok := agronomy.SusceptibilityFor(crop, th.Stage, in.CalamityType); ok {
				mu.Lock()
				p.Susceptibility = &s
				mu.Unlock()
			} else {
				absent("susceptibility", fmt.Sprintf(
					"no published figure for %s at %s", in.CalamityType, th.Stage))
			}
		}

		st, err := agronomy.StressOver(crop, in.VarietyDuration, days,
			in.SowingDate, in.SowingDate, in.IncidentDate)
		if err != nil {
			absent("heat and cold stress", err.Error())
		} else {
			mu.Lock()
			p.Stress = &st
			mu.Unlock()
		}
		return nil
	})

	// --- the sowing date, checked against the crop's own emergence --------
	run("sowing date check", func() error {
		c, ok := agronomy.CheckSowingDate(in.SowingDate, series)
		if !ok {
			return errors.New("the optical series cannot show emergence, so the " +
				"declared sowing date could not be checked")
		}
		mu.Lock()
		defer mu.Unlock()
		p.SowingCheck = &c
		return nil
	})

	// --- radar ------------------------------------------------------------
	run("radar", func() error {
		if a.Radar == nil {
			return errors.New("no radar source is configured")
		}
		// Radar needs a polygon to average backscatter over, and a parcel
		// AgriStack returns no geometry for has none.
		//
		// Caught here rather than at the radar service, which answers a
		// Pydantic validation error naming a field and a literal -- and that
		// string became the officer's reason for the absent source. The
		// officer does not need to know the request body's shape; they need
		// to know this parcel has never been mapped, which is a thing
		// somebody can act on.
		if in.Geometry.Type == "" || len(in.Geometry.Coordinates) == 0 {
			return errors.New(
				"this parcel has no recorded boundary, so there is no area to " +
					"measure backscatter over; the farmer can trace it from the " +
					"app by walking the field edge")
		}
		from, to := radarWindow(in.SowingDate, in.IncidentDate)
		res, err := a.Radar.SAR(ctx, satellite.SARRequest{
			Geometry:  in.Geometry,
			StartDate: from,
			EndDate:   to,
			FarmerID:  in.FarmerID,
			FieldID:   in.FarmID,
		})
		if err != nil {
			return err
		}

		// The radar service can generate a plausible series with no satellite
		// behind it, so this side of the contract can be developed without an
		// Earth Engine project. It labels those SYNTHETIC in the missions list.
		//
		// Outside development that label is a refusal, not a note. A generated
		// reading is indistinguishable from an observation once it is in the
		// packet: the agent reasons over it as fact and the dossier prints it
		// under a Sentinel-1 heading in a row marked as corroborating. Reported
		// absent is the honest outcome, and it is the outcome the deployment
		// already handles, because a radar module that is switched off produces
		// it every day.
		synthetic := sar.IsSynthetic(res.Missions)
		if synthetic && !a.AllowSynthetic {
			return errors.New(
				"the radar service returned a generated series, which is not evidence")
		}

		obs := make([]sar.Observation, 0, len(res.TimeSeries))
		for _, pt := range res.TimeSeries {
			obs = append(obs, sar.Observation{
				Date: pt.Date, VVdB: pt.VVdB, VHdB: pt.VHdB, Orbit: pt.Orbit,
			})
		}

		if in.OpticalGapFrom != "" && in.OpticalGapTo != "" {
			if n, ok := sar.CoversOpticalGap(obs, in.OpticalGapFrom, in.OpticalGapTo); ok {
				mu.Lock()
				p.RadarCoversGap = n
				mu.Unlock()
			}
		}

		reading, why, ok := sar.Analyse(obs, in.IncidentDate)
		if !ok {
			return errors.New(why)
		}
		// Provenance travels with the reading. Every surface that renders it
		// can then say what it is, instead of having to ask.
		reading.Missions = res.Missions
		reading.Synthetic = synthetic

		mu.Lock()
		defer mu.Unlock()
		p.Radar = reading
		return nil
	})

	// --- canopy water and soil moisture ------------------------------------
	run("moisture", func() error {
		if a.Moisture == nil {
			return errors.New("no moisture source is configured")
		}
		// The same guard as radar, for the same reason: a parcel mean needs a
		// parcel, and the officer needs to know the boundary is what is
		// missing rather than read a validation error about a request body.
		if in.Geometry.Type == "" || len(in.Geometry.Coordinates) == 0 {
			return errors.New(
				"this parcel has no recorded boundary, so there is no area to " +
					"measure leaf water over; the farmer can trace it from the " +
					"app by walking the field edge")
		}
		from, to, ok := moisture.Window(in.SowingDate, in.IncidentDate)
		if !ok {
			return errors.New("the incident date is not a date, so there is no " +
				"window to measure")
		}

		res, err := a.Moisture.Moisture(ctx, satellite.MoistureRequest{
			Geometry:      in.Geometry,
			StartDate:     from,
			EndDate:       to,
			BaselineYears: MoistureBaselineYears,
			FarmerID:      in.FarmerID,
			FieldID:       in.FarmID,
		})
		if err != nil {
			return err
		}

		// Refused outside development, exactly as a generated radar series
		// is. A generated drought reads as an observed one once it is in the
		// packet, and the panel would reason over it as fact.
		if moisture.IsSynthetic(res.Missions) && !a.AllowSynthetic {
			return errors.New(
				"the satellite service returned a generated moisture record, " +
					"which is not evidence")
		}

		reading, why, ok := moisture.Analyse(res, in.IncidentDate, in.CalamityType)
		if !ok {
			return errors.New(why)
		}

		mu.Lock()
		defer mu.Unlock()
		p.Moisture = reading
		return nil
	})

	// --- the survey video -------------------------------------------------
	run("ground vision", func() error {
		if a.Vision == nil {
			return errors.New(
				"no vision pipeline is configured, so the survey video was not " +
					"measured; this is the heaviest input in the specification " +
					"and the assessment proceeds without it")
		}
		if in.ReportID == "" {
			return errors.New("this assessment is not tied to a report, so there " +
				"is no survey video to measure")
		}

		clips, err := a.Vision.Measure(ctx, in.ReportID, in.CropName)
		if err != nil {
			return err
		}
		if len(clips) == 0 {
			return errors.New("no survey video has been captured for this report")
		}

		// Each point scored on its own, so the record shows whether the field
		// was uniformly hurt or hurt in one corner -- which is the difference
		// the directed walk exists to capture.
		parts := make([]groundcv.Measurements, 0, len(clips))
		var stations []groundcv.StationScore
		// The strongest statement any station made about the crop's identity.
		// One corner is enough: a farmer does not grow the declared crop at
		// three points and something else at the fourth, and a walk where one
		// clip identifies a different crop is exactly what an officer should
		// be sent to look at.
		var crop *vision.CropCheck
		for _, clip := range clips {
			parts = append(parts, clip.Measurements)
			if c := clip.Crop; c.Mismatched() &&
				(crop == nil || !crop.Mismatched() || c.Confidence > crop.Confidence) {
				found := c
				crop = &found
			} else if crop == nil && (c.Seen != "" || c.Matches != nil) {
				found := c
				crop = &found
			}
			one, err := groundcv.Evaluate(clip.Measurements)
			if err != nil {
				// A station that cannot be scored -- a lens pointed at the
				// bund -- is left out of the list and out of the total, and
				// the count of measured points says so.
				continue
			}
			stations = append(stations, groundcv.StationScore{
				StationNo:      clip.StationNo,
				SCV:            one.SCV,
				LodgingClass:   one.LodgingClass,
				Quality:        one.Quality,
				Keyframes:      one.Keyframes,
				CropPixelShare: one.CropPixelShare,
			})
		}

		combined, err := groundcv.Combine(parts)
		if err != nil {
			return err
		}

		// A refusal from the scorer is a finding about the footage, not a
		// fault: a walk that is mostly sky cannot be scored, and saying so is
		// better than dividing by whatever crop pixels happened to be there.
		score, err := groundcv.Evaluate(combined)
		if err != nil {
			return err
		}

		if crop != nil {
			mu.Lock()
			p.GroundCrop = crop
			mu.Unlock()
		}
		score.Stations = stations
		score.StationsMeasured = len(stations)
		if len(stations) > 1 {
			lowest, highest := stations[0].SCV, stations[0].SCV
			for _, st := range stations[1:] {
				lowest = min(lowest, st.SCV)
				highest = max(highest, st.SCV)
			}
			score.Spread = highest - lowest
		}

		mu.Lock()
		defer mu.Unlock()
		p.GroundCV = &score
		return nil
	})

	// --- the optical record at the spots that were filmed -----------------
	//
	// Gathered here and compared after the wait below: the comparison needs
	// the vision scores, which another source is fetching at the same moment,
	// and a source that reads another source's output is a race however
	// carefully it is written.
	var spots map[int]PointSample
	run("ndvi at the capture points", func() error {
		if a.Points == nil {
			return errors.New("no per-point optical sampling is configured")
		}
		if in.ReportID == "" {
			return errors.New("this assessment is not against a filed claim, " +
				"so there is no walk to sample")
		}
		if in.RRVL <= 0 && in.PeakNDVI <= 0 {
			return errors.New("the parcel itself has no optical figure to " +
				"compare the points against")
		}
		got, err := a.Points.SpotSeries(ctx, PointRequest{
			ReportID:     in.ReportID,
			FarmID:       in.FarmID,
			FarmerID:     in.FarmerID,
			SowingDate:   in.SowingDate,
			IncidentDate: in.IncidentDate,
			RadiusM:      survey.SamplingRadiusM,
		})
		if err != nil {
			return err
		}
		if len(got) == 0 {
			return errors.New("no recorded point of the walk has ground " +
				"inside the parcel to sample")
		}
		mu.Lock()
		defer mu.Unlock()
		spots = got
		return nil
	})

	// --- what has been claimed here before --------------------------------
	run("claim history", func() error {
		if a.Claims == nil {
			return errors.New("no claim store is configured")
		}
		h, err := a.Claims.For(ctx, claims.Lookup{
			ReportID:        in.ReportID,
			FarmerID:        in.FarmerID,
			FarmID:          in.FarmID,
			SurveyNumber:    in.SurveyNumber,
			VillageCode:     in.VillageCode,
			EventID:         in.EventID,
			DeclaredPercent: in.DeclaredPercent,
		})
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		p.History = h
		return nil
	})

	// --- what an officer counted ------------------------------------------
	run("field measurements", func() error {
		if a.Fieldwork == nil {
			return errors.New("no measurement store is configured")
		}
		if in.ReportID == "" {
			return errors.New("this assessment is not attached to a report")
		}
		ms, err := a.Fieldwork.ForReport(ctx, in.ReportID)
		if err != nil {
			return err
		}
		s := fieldwork.Summarise(ms, in.FieldAreaHa)
		mu.Lock()
		defer mu.Unlock()
		p.Measurements = &s
		return nil
	})

	wg.Wait()

	// The comparison, now that both the patches and the vision scores are in.
	//
	// After the wait rather than inside a source, because it reads two of them.
	// Nothing here can fail: Compare returns nil when there is nothing to say.
	if len(spots) > 0 {
		p.PointNDVI = pointndvi.Compare(pointndvi.Input{
			RadiusM:          survey.SamplingRadiusM,
			FieldRRVLPercent: in.RRVL,
			Spots:            spotsWithFootage(spots, p.GroundCV),
		})
	}

	if p.Denominators != nil {
		p.Denominators.Absent = p.Absent
	}
	p.Elapsed = time.Since(started).Round(time.Millisecond).String()
	return p
}

// radarWindowDays is how far either side of the incident radar is asked for.
//
// Wide enough to hold several passes at a six-to-twelve-day revisit, so a
// baseline can be an average rather than a single day's soil moisture, and
// narrow enough that the engine is not asked to process a whole season.
const radarWindowDays = 40

func radarWindow(sowing, incident string) (string, string) {
	inc, err := time.Parse("2006-01-02", incident)
	if err != nil {
		return sowing, incident
	}
	from := inc.AddDate(0, 0, -radarWindowDays)
	if sow, err := time.Parse("2006-01-02", sowing); err == nil && sow.After(from) {
		from = sow
	}
	return from.Format("2006-01-02"), inc.AddDate(0, 0, radarWindowDays).Format("2006-01-02")
}

func (p *Packet) ensureDenominators() *baseline.Denominators {
	if p.Denominators == nil {
		p.Denominators = &baseline.Denominators{}
	}
	return p.Denominators
}

func (a *Assembler) logf(msg string, args ...any) {
	if a.Log != nil {
		a.Log.Warn(msg, args...)
	}
}

// Grounded reports whether the figure rests on anything outside the claim.
func (p *Packet) Grounded() bool {
	if p == nil {
		return false
	}
	if p.Denominators.Grounded() {
		return true
	}
	if p.Radar != nil && len(p.Radar.Findings) > 0 {
		return true
	}
	if p.Measurements != nil && p.Measurements.SupportsAFigure {
		return true
	}
	// A measured survey video is the strongest grounding of the lot: it is the
	// only evidence in the packet that looked at the crop rather than at the
	// field it was standing in.
	if p.GroundCV != nil {
		return true
	}
	return false
}

// Summary is what an officer reads before any of the detail.
func (p *Packet) Summary() string {
	if p == nil {
		return "No supporting evidence was gathered."
	}
	if !p.Grounded() {
		return "This assessment rests on the claim and the photographs alone: " +
			"no baseline, no radar and no field measurements were available. " +
			"Read the percentage as a judgement."
	}
	return p.Denominators.Summary()
}

// CropMismatch is the fraud indicator for a field that does not hold the crop
// it was claimed for, or nil where there is nothing to say.
//
// # Why this is a backend indicator rather than the panel's
//
// The panel can raise fraud indicators and sometimes does. This one is
// computed here, from the measurement, because it must not depend on a model
// choosing to mention it in prose: the walk is the only evidence that can
// answer the question at all, and an answer that appears only when the
// synthesiser happens to repeat it is not a check.
//
// # Why severity is not always critical
//
// Critical routes the claim to RED and holds it for investigation, which is
// the right outcome for a confident identification of a different crop. It is
// the wrong outcome for a model that is unsure, because the cost of being
// wrong falls on a farmer whose field really does hold what they declared.
// So the model's own confidence decides, and either way the claim carries a
// flag an officer reads.
func (p *Packet) CropMismatch() *ai.FraudIndicator {
	if p == nil || p.GroundCrop == nil || !p.GroundCrop.Mismatched() {
		return nil
	}
	c := p.GroundCrop

	detail := fmt.Sprintf("the claim names %s; the survey video shows %s",
		orUnknown(c.Declared), orUnknown(c.Seen))
	if c.Detail != "" {
		detail += ". " + c.Detail
	}
	detail += fmt.Sprintf(" (vision model, confidence %.0f%%; the officers' own"+
		" inspection settles it)", c.Confidence*100)

	severity := ai.FraudWarning
	if c.Confidence >= CropMismatchCritical {
		severity = ai.FraudCritical
	}
	return &ai.FraudIndicator{
		Code:     "CROP_NOT_AS_DECLARED",
		Severity: severity,
		Detail:   detail,
	}
}

// CropMismatchCritical is the confidence at which a crop mismatch holds the
// claim for investigation rather than merely flagging it.
const CropMismatchCritical = 0.80

func orUnknown(s string) string {
	if s == "" {
		return "an unidentified crop"
	}
	return s
}

// spotsWithFootage pairs each sampled patch with what the clip taken there
// measured.
//
// Matched on the point's own number, never on position in a list: a walk whose
// second clip failed to score produces three vision readings, and pairing the
// third of those with the second patch would put an allegation against the
// wrong piece of ground.
func spotsWithFootage(
	samples map[int]PointSample, ground *groundcv.Score,
) []pointndvi.Spot {
	footage := map[int]float64{}
	if ground != nil {
		for _, st := range ground.Stations {
			if st.StationNo > 0 && st.Keyframes > 0 {
				footage[st.StationNo] = st.SCV
			}
		}
	}

	// Ordered, so the report and the panel see the points in walk order rather
	// than in Go's map order, which changes between runs of the same
	// assessment.
	nos := make([]int, 0, len(samples))
	for no := range samples {
		nos = append(nos, no)
	}
	sort.Ints(nos)

	out := make([]pointndvi.Spot, 0, len(nos))
	for _, no := range nos {
		s := samples[no]
		spot := pointndvi.Spot{
			StationNo:     no,
			AreaM2:        s.AreaM2,
			OutsideParcel: s.OutsideParcel,
			Series:        s.Series,
		}
		if scv, ok := footage[no]; ok {
			spot.FootageSCV = &scv
		}
		out = append(out, spot)
	}
	return out
}

// cropYearStart is the calendar year the season began in.
//
// A crop year is written "2026-27" and the published rainfall is filed under the
// year the monsoon started, so the first four digits are what is wanted. Where
// no crop year was recorded the sowing date settles it, and a season sown in
// January belongs to the previous monsoon.
func cropYearStart(cropYear, sowingDate string) string {
	if len(cropYear) >= 4 {
		return cropYear[:4]
	}
	if t, err := time.Parse("2006-01-02", sowingDate); err == nil {
		if t.Month() < time.June {
			return strconv.Itoa(t.Year() - 1)
		}
		return strconv.Itoa(t.Year())
	}
	return ""
}
