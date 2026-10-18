package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// reportStations serves the four points the farmer is to walk to.
//
// The app asks for these once, at the start of a report, and then guides
// between them. They do not change while the walk is happening -- see
// survey.Store.Stations for why that matters and for the single exception.
func (s *Server) reportStations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get() applies the caller's scope, so a farmer reaches their own report
	// and an officer reaches the ones in their jurisdiction. Nothing here has
	// to re-derive that.
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"),
		rbac.FromContext(ctx).Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// Stations are placed on the parcel the report names first.
	//
	// A report can carry several crop lines across several survey numbers,
	// and four stations cannot sample all of them. Placing them on the first
	// parcel is a stated limitation rather than a silent one: the report says
	// which parcel was walked, so nobody reads coverage of one survey number
	// as coverage of three.
	farmIDs := rep.FarmIDs()
	if len(farmIDs) == 0 {
		writeError(w, http.StatusConflict, "NO_PARCEL",
			"this report names no registry parcel, so there is nowhere to sample")
		return
	}

	stations, rebuilt, err := s.Captures.StationsWithRebuild(
		ctx, rep.ID, farmIDs[0], s.downhillFor(ctx, farmIDs[0]))
	if errors.Is(err, survey.ErrNoBoundary) {
		// The ordinary state of a real parcel: AgriStack returns no geometry,
		// so nobody has drawn this field yet. Distinct from a server fault
		// because the app acts on it -- it sends the farmer to walk the
		// boundary, after which this route answers normally.
		writeError(w, http.StatusConflict, "NO_BOUNDARY",
			"this parcel has no boundary yet; trace it before surveying")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STATIONS_UNAVAILABLE",
			"could not work out where to sample this parcel")
		return
	}

	// A replaced set is recorded as its own action. The report claims the
	// sampling plan was fixed before the walk began; the one operation that can
	// move it afterwards must be visible to an auditor rather than inferred
	// from a timestamp.
	action := "report.stations"
	if rebuilt {
		action = "report.stations.rebuilt"
	}

	s.Audit.Record(ctx, audit.Event{
		Action: action, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
	}.FromContext(ctx))

	// Best effort: a farmer standing in a field is not stopped from walking
	// because this could not be read. Missing it costs a refusal on arrival,
	// which is the behaviour before this existed.
	taken, err := s.Captures.TakenPoints(ctx, farmIDs[0], rep.ID, rep.FarmerID)
	if err != nil {
		taken = nil
	}

	writeJSON(w, http.StatusOK, stationsResponse{
		ReportID:    rep.ID,
		FarmID:      farmIDs[0],
		Stations:    stations,
		TakenPoints: taken,
	})
}

type stationsResponse struct {
	// Empty on the parcel-scoped route, where no report exists yet.
	ReportID string           `json:"report_id,omitempty"`
	FarmID   string           `json:"farm_id"`
	Stations []survey.Station `json:"stations"`

	// Points on this survey number that other claims have already recorded, so
	// the handset can refuse them before the farmer records rather than after
	// they have walked home. See survey.TakenPoints.
	TakenPoints []survey.TakenPoint `json:"taken_points,omitempty"`
}

// downhillFor is the direction water runs on a parcel, where that is known.
//
// Nil is the common answer and not a failure. It means one of three things,
// none of which should stop a farmer walking: no assessment has run yet, the
// terrain module was unavailable, or the parcel is too flat to have a
// downhill at all. Station placement takes nil and spaces the four points
// along the parcel's long axis instead.
//
// Note this is only consulted when a station set is first frozen. An
// assessment that completes mid-walk does not reorder the stations somebody
// is already walking between.
func (s *Server) downhillFor(ctx context.Context, farmID string) *float64 {
	if s.Store == nil {
		return nil
	}
	terrain, ok := s.Store.TerrainFor(ctx, farmID)
	if !ok {
		return nil
	}
	return terrain.DownhillBearing()
}

// parcelStations serves the four points for a parcel, with no report needed.
//
// # Why this exists alongside the report-scoped route
//
// The handset is offline-first: a draft lives on the phone until there is
// signal, so at the moment a farmer walks their field the report does not
// exist on the server and has no id to ask about. Fetching stations by report
// id would mean a farmer in a field with no bars could never be told where to
// stand -- which is every farmer, in the place this is used.
//
// Placement is deterministic given a boundary and an aspect, so the points do
// not depend on the report at all. The app asks for them when it fetches the
// parcel, which is a step that already needs a network, and carries them into
// the walk. The server still placed them, still before the walk began, so the
// report's claim is unchanged: the sampling plan was not the farmer's. What the
// report does not claim, and must not, is that the farmer stood on the points.
// They are suggestions; the walk enforces the boundary and the spacing between
// recordings (survey.SpacingFor), and nothing else.
//
// The report-scoped route stays: it is what freezes a set against a report and
// records which boundary version it was built from.
func (s *Server) parcelStations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	farmID := r.PathValue("farmID")

	// Whose parcel this is, before saying where to stand on it. The same
	// check as the boundary route, which is where the gap this inherited was
	// fixed: parcel:read is held by every farmer and is not scoped to a
	// jurisdiction, so without this a signed-in farmer could read the sampling
	// coordinates of anybody's land from its farm id.
	if err := s.mayReadParcel(w, r); err != nil {
		return
	}

	// The extent of the crop about to be walked, which is what sizes the walk.
	//
	// From the caller, because only the handset knows it: the farmer has just
	// entered the sown area on the form, and the parcel cannot supply it -- a
	// survey number is routinely shared between crops and between farmers, so its
	// polygon says nothing about how much of it this claim is for.
	//
	// Absent or unreadable is not an error. It falls back to the parcel rule,
	// which is what every claim used before this, rather than refusing to tell a
	// farmer standing in a field where to stand.
	sownHa, _ := strconv.ParseFloat(r.URL.Query().Get("sown_ha"), 64)
	if sownHa < 0 || math.IsNaN(sownHa) || math.IsInf(sownHa, 0) {
		sownHa = 0
	}

	stations, err := s.Captures.PlaceStations(
		ctx, farmID, s.downhillFor(ctx, farmID), sownHa)
	if errors.Is(err, survey.ErrNoBoundary) {
		writeError(w, http.StatusConflict, "NO_BOUNDARY",
			"this parcel has no boundary yet; trace it before surveying")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "STATIONS_UNAVAILABLE",
			"could not work out where to sample this parcel")
		return
	}

	// Every point already recorded on this parcel, whoever recorded it. The claim
	// does not exist on the server yet, so there is no report to exclude -- and
	// nothing on the parcel belongs to it.
	//
	// Whose it is decides the wording, so the signed-in farmer is passed in: their
	// own udad claim is a different sentence from a neighbour's.
	taken, err := s.Captures.TakenPoints(ctx, farmID, "", actingFarmer(r))
	if err != nil {
		taken = nil
	}

	writeJSON(w, http.StatusOK, stationsResponse{
		FarmID:      farmID,
		Stations:    stations,
		TakenPoints: taken,
	})
}

// actingFarmer is whose claim this request is for.
//
// Not always whoever signed in: a handset acts for a second farmer through a
// grant, and the route carries that farmer's id. Used only to word a refusal --
// "your own udad claim recorded this" rather than "another farmer did" -- so
// falling back to the account holder is a wording that is merely less specific,
// never a disclosure: the points themselves are already this parcel's.
func actingFarmer(r *http.Request) string {
	if id := r.URL.Query().Get("farmer_id"); id != "" {
		return id
	}
	if p := rbac.FromContext(r.Context()); p != nil {
		return p.FarmerID
	}
	return ""
}
