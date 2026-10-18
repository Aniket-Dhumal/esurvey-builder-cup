package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// Walking a plot boundary, and using what was walked.
//
// # Why this endpoint is the one that unblocks the rest
//
// AgriStack returns plot_geometry: null for every farmer this platform can
// reach. Without a boundary the assessment refuses outright, the geofence on
// every photograph has nothing to check against, and the prior-season and
// neighbourhood baselines have no parcel to read. Everything else built here
// stands on a field boundary that no live farmer had until this.

type traceRequest struct {
	Points []survey.TracePoint `json:"points"`
}

type traceResponse struct {
	Boundary *survey.Boundary `json:"boundary"`
	Trace    *survey.Trailing `json:"trace"`

	// Message is what to tell the person who just walked their field, in
	// their own terms rather than in hectares of polygon.
	Message string `json:"message"`
}

// traceBoundary records a farmer's walk of their own parcel.
func (s *Server) traceBoundary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	farmID := r.PathValue("farmID")

	var req traceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	// The parcel has to be theirs. A farmer tracing somebody else's survey
	// number would put a boundary under another person's claim, and the
	// registry is the only thing that can say whose parcel this is.
	rec, parcel, err := s.parcelOf(r, p, r.URL.Query().Get("farmer_id"), farmID)
	if err != nil {
		s.writeRegistryError(w, err)
		return
	}

	extent, _ := parcel.ExtentHectares()

	b, tr, err := s.Captures.RecordTrace(ctx, survey.Trace{
		FarmID:           parcel.FarmID,
		FarmerID:         rec.Farmer.FarmerID,
		SurveyNumber:     parcel.SurveyNumber,
		VillageLGDCode:   parcel.VillageLGDCode,
		TracedBy:         p.UserID,
		Points:           req.Points,
		RegistryExtentHa: extent,
	})
	switch {
	case errors.Is(err, survey.ErrTooFewPoints),
		errors.Is(err, survey.ErrNotAPolygon),
		errors.Is(err, survey.ErrImplausible):
		writeError(w, http.StatusUnprocessableEntity, "BAD_BOUNDARY", err.Error())
		return
	case errors.Is(err, survey.ErrTooCoarse):
		// Not the farmer's fault and fixable by standing still, so it is worth
		// distinguishing from a boundary that is simply wrong.
		writeError(w, http.StatusUnprocessableEntity, "POSITION_TOO_COARSE", err.Error())
		return
	case errors.Is(err, survey.ErrNotYourParcel):
		writeError(w, http.StatusConflict, "REGISTRY_BOUNDARY_EXISTS", err.Error())
		return
	case err != nil:
		s.Log.Error("recording a traced boundary", "farm_id", farmID, "error", err)
		writeError(w, http.StatusInternalServerError, "TRACE_FAILED",
			"the boundary could not be recorded")
		return
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionBoundaryTraced, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "parcel_boundary", ResourceID: parcel.FarmID,
		JurisdictionCode: parcel.VillageLGDCode,
		Detail: map[string]any{
			"corners":          len(req.Points),
			"area_ha":          b.PolygonAreaHa,
			"worst_accuracy_m": tr.WorstAccuracyM,
			"registry_extent":  extent,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusCreated, traceResponse{
		Boundary: b, Trace: tr,
		Message: "Your plot boundary is recorded. Photographs you take from " +
			"now on will be checked against it, and the assessment can read " +
			"this field from satellite.",
	})
}

// confirmBoundary records an officer agreeing a traced plot is the plot.
func (s *Server) confirmBoundary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	farmID := r.PathValue("farmID")

	// Only a parcel in the officer's own area. There was no check at all: any
	// verifying officer, in any district or state, could put their name to a
	// farmer's boundary -- and a confirmed boundary is what the geofence and
	// the satellite figures stand on.
	village, err := s.Captures.VillageOfParcel(ctx, farmID)
	if err != nil || village == "" {
		writeError(w, http.StatusNotFound, "NO_TRACED_BOUNDARY",
			"there is no farmer-traced boundary on this parcel to confirm")
		return
	}
	if err := s.assertInScope(r, p, village, "parcel", farmID); err != nil {
		writeError(w, http.StatusNotFound, "NO_TRACED_BOUNDARY",
			"there is no farmer-traced boundary on this parcel to confirm")
		return
	}

	if err := s.Captures.Confirm(ctx, farmID, p.UserID); err != nil {
		if errors.Is(err, survey.ErrNoBoundary) {
			writeError(w, http.StatusNotFound, "NO_TRACED_BOUNDARY",
				"there is no farmer-traced boundary on this parcel to confirm")
			return
		}
		s.Log.Error("confirming a boundary", "farm_id", farmID, "error", err)
		writeError(w, http.StatusInternalServerError, "CONFIRM_FAILED",
			"the confirmation could not be recorded")
		return
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionBoundaryConfirmed, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "parcel_boundary", ResourceID: farmID,
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, map[string]any{
		"farm_id":   farmID,
		"confirmed": true,
		"message": "Recorded. Figures derived from this boundary now carry " +
			"that an officer stood in the field and agreed it.",
	})
}
