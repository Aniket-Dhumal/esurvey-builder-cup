package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/survey"
)

// Resumable upload, in three routes.
//
//	POST .../uploads              declare a file, get an id and a chunk number
//	PUT  .../uploads/{id}/{n}     send chunk n
//	GET  .../uploads/{id}         ask which chunk to send next
//
// The GET is the whole reason this exists. A handset that lost signal at 80%
// asks where it got to and carries on, rather than starting a fifty-megabyte
// video again on a connection that has already proved it cannot finish one.

type beginUploadRequest struct {
	// The handset's own key for this file. Sending the same key twice
	// resumes rather than starting a second upload.
	UploadKey string `json:"upload_key"`

	ContentType string `json:"content_type"`
	TotalBytes  int64  `json:"total_bytes"`
	ChunkSize   int    `json:"chunk_size"`
	SHA256      string `json:"sha256"`

	Kind      string `json:"kind"`
	LineNo    *int   `json:"line_no,omitempty"`
	Caption   string `json:"caption,omitempty"`
	StationNo *int   `json:"station_no,omitempty"`

	CapturedAt time.Time `json:"captured_at"`
	Lat        float64   `json:"lat"`
	Lon        float64   `json:"lon"`
	AccuracyM  *float64  `json:"accuracy_m,omitempty"`
	HeadingDeg *float64  `json:"heading_deg,omitempty"`
	DurationMs *int      `json:"duration_ms,omitempty"`
	IsMocked   *bool     `json:"is_mocked,omitempty"`

	// What the handset's encoder agreed to. Optional: a build that does not send
	// it leaves the columns null rather than having a profile invented for it.
	EncodingProfile    *string `json:"encoding_profile,omitempty"`
	EncodingFPS        *int    `json:"encoding_fps,omitempty"`
	EncodingBitrateBPS *int    `json:"encoding_bitrate_bps,omitempty"`

	// What the live assistant asked the farmer to show in this clip, and what said
	// it. Absent for most clips, which is what null in the column means.
	GuidanceAskedFor *string `json:"guidance_asked_for,omitempty"`
	GuidanceModel    *string `json:"guidance_model,omitempty"`
	DeviceLabel      string  `json:"device_label,omitempty"`
}

func (s *Server) beginUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	scope, err := s.actingScope(r, rbac.FromContext(ctx))
	if err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), scope)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	var req beginUploadRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"the upload request could not be read")
		return
	}

	in := survey.New{
		ReportID:    rep.ID,
		LineNo:      req.LineNo,
		Kind:        survey.Kind(req.Kind),
		Caption:     req.Caption,
		ContentType: req.ContentType,
		CapturedAt:  req.CapturedAt,
		Lat:         req.Lat,
		Lon:         req.Lon,
		AccuracyM:   req.AccuracyM,
		HeadingDeg:  req.HeadingDeg,
		StationNo:   req.StationNo,
		DurationMs:  req.DurationMs,
		IsMocked:    req.IsMocked,
		DeviceLabel: req.DeviceLabel,

		EncodingProfile:    req.EncodingProfile,
		EncodingFPS:        req.EncodingFPS,
		EncodingBitrateBPS: req.EncodingBitrateBPS,

		GuidanceAskedFor: req.GuidanceAskedFor,
		GuidanceModel:    req.GuidanceModel,
	}
	if len(rep.FarmIDs()) > 0 {
		in.FarmID = rep.FarmIDs()[0]
	}
	if p := rbac.FromContext(ctx); p != nil {
		in.CapturedBy = p.UserID
	}

	st, err := s.Captures.BeginUpload(ctx, in, req.UploadKey,
		req.TotalBytes, req.ChunkSize, req.SHA256)
	if err != nil {
		s.writeUploadError(w, err)
		return
	}

	s.Audit.Record(ctx, audit.Event{
		Action: "capture.upload.begin", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
	}.FromContext(ctx))

	writeJSON(w, http.StatusAccepted, st)
}

// uploadStatus answers "which chunk do you want next".
func (s *Server) uploadStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	scope, err := s.actingScope(r, rbac.FromContext(ctx))
	if err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), scope)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	st, err := s.Captures.ResumeUpload(ctx, rep.ID, r.PathValue("uploadID"))
	if err != nil {
		s.writeUploadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// putChunk takes one chunk as a raw body.
//
// Raw rather than multipart: the body is already opaque bytes, and base64 in
// JSON would add a third to every chunk on the connection least able to
// afford it.
func (s *Server) putChunk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	scope, err := s.actingScope(r, rbac.FromContext(ctx))
	if err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), scope)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil || index < 0 {
		writeError(w, http.StatusBadRequest, "BAD_CHUNK_INDEX",
			"the chunk index must be a whole number")
		return
	}

	body := http.MaxBytesReader(w, r.Body, survey.MaxChunkBytes+1)
	defer body.Close()

	st, err := s.Captures.AppendChunk(ctx, rep.ID, r.PathValue("uploadID"), index, body)
	if err != nil {
		s.writeUploadError(w, err)
		return
	}

	if st.Complete() {
		s.Audit.Record(ctx, audit.Event{
			Action: "capture.upload.complete", Outcome: audit.OutcomeAllow,
			ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
			ResourceType: "survey_capture", ResourceID: st.CaptureID,
			JurisdictionCode: rep.Place.VillageCode,
		}.FromContext(ctx))

		writeJSON(w, http.StatusCreated, st)
		return
	}

	writeJSON(w, http.StatusOK, st)
}

// writeUploadError turns a store error into something the app can act on.
//
// The distinctions matter more here than elsewhere, because the app decides
// what to do next from the status alone and the farmer is not watching. A
// refusal it should stop retrying and a fault it should retry look the same
// unless they are separated deliberately.
func (s *Server) writeUploadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, survey.ErrUploadNotFound):
		writeError(w, http.StatusNotFound, "UPLOAD_NOT_FOUND",
			"no upload in progress with that id")

	case errors.Is(err, survey.ErrOutsideParcel):
		// Permanent, and worth the specific message: the farmer can walk back
		// into their own field and try again, which no generic refusal tells
		// them.
		writeError(w, http.StatusUnprocessableEntity, "OUTSIDE_PARCEL", err.Error())

	case errors.Is(err, survey.ErrMockedPosition):
		writeError(w, http.StatusUnprocessableEntity, "MOCKED_POSITION",
			"this position was reported by a mock location provider")

	case errors.Is(err, survey.ErrSamePosition):
		// The walk did not move. Permanent for this position and fixed by
		// walking on, which is what the message has to say.
		writeError(w, http.StatusUnprocessableEntity, "SAME_POSITION",
			"record this point somewhere else in the field: "+err.Error())

	case errors.Is(err, survey.ErrPointTakenInSurvey):
		// Permanent for this position, and fixed only by walking to a different
		// part of the survey number -- the farmer's own share of it. The app
		// offers the camera again rather than a retry, which is the whole point
		// of keeping this apart from SAME_POSITION.
		writeError(w, http.StatusUnprocessableEntity, "POINT_TAKEN_IN_SURVEY",
			err.Error())

	case errors.Is(err, survey.ErrAwayFromField):
		writeError(w, http.StatusUnprocessableEntity, "AWAY_FROM_FIELD",
			"this claim's evidence was gathered elsewhere: "+err.Error())

	case errors.Is(err, survey.ErrUploadRestarted):
		// 409, not 422. A 4xx the app reads as a verdict would mark the
		// farmer's video failed for good; the server has already reset the
		// upload, and the right response is to send it again from the start.
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusConflict, "UPLOAD_RESTARTED", err.Error())

	case errors.Is(err, survey.ErrBusy):
		w.Header().Set("Retry-After", "15")
		writeError(w, http.StatusServiceUnavailable, "ASSEMBLY_BUSY", err.Error())

	case errors.Is(err, survey.ErrUploadMismatch):
		// The bytes that arrived are not the bytes that were promised. The
		// app should start the upload again rather than retry a chunk.
		writeError(w, http.StatusUnprocessableEntity, "UPLOAD_CORRUPT", err.Error())

	case errors.Is(err, survey.ErrTooManyUploads):
		// 429 rather than 422: the app should back off and retry once the
		// uploads it already has in flight have finished, which they will.
		writeError(w, http.StatusTooManyRequests, "TOO_MANY_UPLOADS", err.Error())

	case errors.Is(err, survey.ErrChunkTooLarge), errors.Is(err, survey.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "TOO_LARGE", err.Error())

	case errors.Is(err, survey.ErrBadMedia), errors.Is(err, survey.ErrDuplicate):
		writeError(w, http.StatusUnprocessableEntity, "UNUSABLE_MEDIA", err.Error())

	default:
		// Logged, because everything above is a case somebody thought about
		// and this is the one that is not: an operator seeing UPLOAD_FAILED
		// needs the reason, and the farmer must not be given it.
		s.Log.Error("upload failed", "error", err)
		writeError(w, http.StatusInternalServerError, "UPLOAD_FAILED",
			"the upload could not be recorded")
	}
}
