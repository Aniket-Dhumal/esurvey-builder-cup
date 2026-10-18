package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// maxUploadBytes bounds one multipart request: the photograph plus its fields.
const maxUploadBytes = survey.MaxBytes + (64 << 10)

// submitCapture takes one photograph from the field.
//
// multipart rather than a base64 JSON body: a phone photograph is hundreds of
// kilobytes, base64 inflates it by a third, and a farmer on a village
// connection pays for every one of those bytes.
func (s *Server) submitCapture(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	scope, err := s.actingScope(r, p)
	if err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), scope)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// A farmer captures for a report belonging to a farmer they may act for.
	// An officer captures for one in a village they are posted to.
	//
	// "May act for" and not "is": one handset now acts for several farmers --
	// a household shares a phone, and a farmer whose own AgriStack number is
	// long gone borrows one -- and an account that signed in on a number
	// nobody has enrolled has no farmer id of its own at all. Comparing the
	// report against p.FarmerID refused both. The videos went up regardless,
	// because the resumable upload route asks actingScope the same question
	// and takes its answer; this route asked a different one, so a walked
	// claim arrived with its clips and none of its photographs.
	if p.IsFarmer() {
		if acting, err := s.actingFarmer(r, p, rep.FarmerID); err != nil ||
			acting != rep.FarmerID {
			writeError(w, http.StatusForbidden, "NOT_YOUR_REPORT",
				"you can only add evidence to a farmer you have confirmed")
			return
		}
	} else if err := s.assertInScope(r, p, rep.Place.VillageCode,
		"crop_loss_report", rep.ID); err != nil {
		s.writeScopeError(w, err)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "UPLOAD_TOO_LARGE",
			"the image is larger than this system accepts; 8 MB is the limit")
		return
	}

	file, header, err := r.FormFile("image")
	if err != nil {
		writeError(w, http.StatusBadRequest, "MISSING_IMAGE",
			"attach the photograph as the 'image' field")
		return
	}
	defer file.Close()

	lat, latErr := strconv.ParseFloat(r.FormValue("lat"), 64)
	lon, lonErr := strconv.ParseFloat(r.FormValue("lon"), 64)
	if latErr != nil || lonErr != nil {
		writeError(w, http.StatusBadRequest, "MISSING_POSITION",
			"lat and lon are required; they come from the handset at the moment "+
				"of capture, not from the image file")
		return
	}

	capturedAt := time.Now().UTC()
	if raw := r.FormValue("captured_at"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			capturedAt = t
		}
	}

	in := survey.New{
		ReportID:    rep.ID,
		FarmID:      captureFarmID(r, rep),
		Kind:        survey.Kind(r.FormValue("kind")),
		Caption:     r.FormValue("caption"),
		ContentType: contentTypeOf(header.Header.Get("Content-Type")),
		Media:       file,
		CapturedAt:  capturedAt,
		Lat:         lat,
		Lon:         lon,
		DeviceLabel: r.FormValue("device"),
		CapturedBy:  p.UserID,
		IncidentAt:  rep.OccurredAt,
	}
	if v, err := strconv.ParseFloat(r.FormValue("accuracy_m"), 64); err == nil {
		in.AccuracyM = &v
	}
	if v, err := strconv.ParseFloat(r.FormValue("heading_deg"), 64); err == nil {
		in.HeadingDeg = &v
	}
	if v, err := strconv.Atoi(r.FormValue("line_no")); err == nil && v > 0 {
		in.LineNo = &v
	}
	readStationFields(r.FormValue, &in)
	if ev, err := s.Events.Get(ctx, rep.SurveyEventID); err == nil {
		in.WindowStart, in.WindowEnd = ev.IncidentStart, ev.IncidentEnd
	}

	cap, err := s.Captures.Submit(ctx, in)
	if err != nil {
		s.writeCaptureError(w, err)
		return
	}

	// The same photograph, sent again after a lost reply. One photograph, one
	// audit entry, and 200 rather than 201 because nothing was created.
	if cap.Replayed {
		w.Header().Set(headerReplayed, "true")
		writeJSON(w, http.StatusOK, map[string]any{
			"capture":           cap,
			"trusted":           cap.Trusted(),
			"gate_explanations": survey.GateExplanations,
		})
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "capture.submitted", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "survey_capture", ResourceID: cap.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"report":       rep.Reference,
			"kind":         string(cap.Kind),
			"gates_passed": cap.GatesPassed,
			"gates_failed": cap.GatesFailed,
			"inside":       cap.InsideParcel,
			"distance_m":   cap.DistanceFromParcelM,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusCreated, map[string]any{
		"capture":           cap,
		"trusted":           cap.Trusted(),
		"gate_explanations": survey.GateExplanations,
	})
}

// listCaptures returns the evidence on a report.
func (s *Server) listCaptures(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	scope, err := s.actingScope(r, p)
	if err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), scope)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	caps, err := s.Captures.ForReport(ctx, rep.ID)
	if err != nil {
		s.writeCaptureError(w, err)
		return
	}

	flagged := 0
	for _, c := range caps {
		if !c.Trusted() {
			flagged++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"captures":          caps,
		"total":             len(caps),
		"flagged":           flagged,
		"gate_explanations": survey.GateExplanations,
	})
}

// captureMedia streams one photograph.
func (s *Server) captureMedia(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	scope, err := s.actingScope(r, p)
	if err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), scope)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// The capture must belong to the report the caller was scoped against, or
	// an id from one village would fetch a photograph from another.
	caps, err := s.Captures.ForReport(ctx, rep.ID)
	if err != nil {
		s.writeCaptureError(w, err)
		return
	}
	id := r.PathValue("captureID")
	found := false
	for _, c := range caps {
		if c.ID == id {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "CAPTURE_NOT_FOUND",
			"no such capture on this report")
		return
	}

	rc, contentType, err := s.Captures.Media(ctx, id)
	if err != nil {
		s.writeCaptureError(w, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", contentType)
	// Private: this is a photograph of an identified farmer's land.
	w.Header().Set("Cache-Control", "private, max-age=300")
	if _, err := io.Copy(w, rc); err != nil {
		s.Log.Warn("streaming capture", "id", id, "error", err)
	}
}

// parcelBoundary returns a parcel's recorded geometry, for a map and for the
// capture screen to say whether a geofence is even possible.
func (s *Server) parcelBoundary(w http.ResponseWriter, r *http.Request) {
	// Whose parcel this is, before saying where it is.
	//
	// parcel:read is not jurisdiction-scoped, and this route returned the
	// outline of any parcel to anybody holding it -- which every farmer does.
	// A farm id is an AgriStack identifier printed on documents, so "you need
	// the id" was never a control: a signed-in farmer could read the precise
	// shape and position of a neighbour's land.
	//
	// The rule is the one the write path has always used (see parcelOf): a
	// farmer reaches the parcels of the farmers they may act for, and an
	// officer reaches the villages they are posted to.
	if err := s.mayReadParcel(w, r); err != nil {
		return
	}

	b, err := s.Captures.BoundaryFor(r.Context(), r.PathValue("farmID"))
	if errors.Is(err, survey.ErrNoBoundary) {
		writeJSON(w, http.StatusOK, map[string]any{
			"farm_id":  r.PathValue("farmID"),
			"boundary": nil,
			"note": "no boundary is recorded for this parcel, so a photograph " +
				"cannot be checked against it. AgriStack returns no geometry for " +
				"any farmer we can reach.",
		})
		return
	}
	if err != nil {
		s.writeCaptureError(w, err)
		return
	}

	// How the boundary came to be, alongside it.
	//
	// One route rather than two: the geofence, the assessment and the
	// farmer's own screen all ask the same question, and a second endpoint
	// returning the same row with more fields is how two callers end up
	// disagreeing about what a parcel's boundary is.
	out := map[string]any{"boundary": b}
	if geo, trace, err := s.Captures.TracedBoundary(r.Context(), r.PathValue("farmID")); err == nil {
		out["geometry"] = json.RawMessage(geo)
		out["trace"] = trace
	}
	writeJSON(w, http.StatusOK, out)
}

// captureFarmID picks the parcel a capture is of.
//
// A report can span several survey numbers, so the client names one; where it
// does not, the report's first parcel is assumed, which is right for the
// single-parcel case that is almost all of them.
func captureFarmID(r *http.Request, rep *report.Report) string {
	if v := r.FormValue("farm_id"); v != "" {
		for _, id := range rep.FarmIDs() {
			if id == v {
				return v
			}
		}
	}
	if ids := rep.FarmIDs(); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

func contentTypeOf(declared string) string {
	switch declared {
	case "image/jpeg", "image/png", "image/webp":
		return declared
	case "image/jpg":
		return "image/jpeg"
	default:
		return "image/jpeg"
	}
}

func (s *Server) writeCaptureError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, survey.ErrNotFound):
		writeError(w, http.StatusNotFound, "CAPTURE_NOT_FOUND", err.Error())
	case errors.Is(err, survey.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "UPLOAD_TOO_LARGE", err.Error())
	case errors.Is(err, survey.ErrBadMedia):
		writeError(w, http.StatusBadRequest, "BAD_MEDIA", err.Error())
	case errors.Is(err, survey.ErrNoBoundary):
		writeError(w, http.StatusUnprocessableEntity, "NO_BOUNDARY", err.Error())

	// The fence's verdicts, answered as the clip upload answers them.
	//
	// They fell through to the 500 below. A 500 tells the handset the office
	// failed and to try again, so a photograph taken away from the field was
	// resent on every pass, for ever, and the farmer was never told the one
	// thing they could act on: walk back into their own field.
	case errors.Is(err, survey.ErrOutsideParcel):
		writeError(w, http.StatusUnprocessableEntity, "OUTSIDE_PARCEL", err.Error())
	case errors.Is(err, survey.ErrMockedPosition):
		writeError(w, http.StatusUnprocessableEntity, "MOCKED_POSITION",
			"this position was reported by a mock location provider")
	case errors.Is(err, survey.ErrSamePosition):
		writeError(w, http.StatusUnprocessableEntity, "SAME_POSITION",
			"record this point somewhere else in the field: "+err.Error())
	case errors.Is(err, survey.ErrAwayFromField):
		writeError(w, http.StatusUnprocessableEntity, "AWAY_FROM_FIELD",
			"this claim's evidence was gathered elsewhere: "+err.Error())
	case errors.Is(err, survey.ErrDuplicate):
		writeError(w, http.StatusUnprocessableEntity, "UNUSABLE_MEDIA", err.Error())
	default:
		s.Log.Error("capture operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "CAPTURE_ERROR",
			"the request could not be completed")
	}
}

// readStationFields takes the walk's own fields off a photograph upload.
//
// # Why this is separate, and why it is tested
//
// The walk records a photograph and then a clip at each of the four points.
// The clip goes up in chunks through beginUpload, which carried the station
// number from the start. The photograph goes up in one multipart request, and
// this end never read the field -- so every still the office holds was filed
// against no point at all, the report drew the map of the walk with four
// photographs it could not place beside it, and nothing anywhere failed.
//
// Given a bare form reader so the parsing can be tested without standing up a
// server, a database and a blob store: what broke here was three missing
// lines, and three missing lines are exactly what a test of the parsing
// catches.
func readStationFields(value func(string) string, in *survey.New) {
	if v, err := strconv.Atoi(value("station_no")); err == nil && v > 0 {
		in.StationNo = &v
	}
	if v, err := strconv.ParseFloat(value("station_offset_m"), 64); err == nil {
		in.StationOffsetM = &v
	}
	// Absent from a handset too old to say stays absent rather than reading as
	// false: "did not tell us" and "told us it was genuine" are different
	// facts, and only one of them is evidence. See survey.New.IsMocked.
	if v, err := strconv.ParseBool(value("is_mocked")); err == nil {
		in.IsMocked = &v
	}
}
