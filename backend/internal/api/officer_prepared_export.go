package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/officeragent"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// downloadPreparedExport serves a document the assistant prepared in a
// conversation.
//
// # Why there is a second export route at all
//
// Because the one next door answers a request and this one answers a sentence.
// POST /export is a console pressing a button: the format and the columns are in
// the request, and the document comes straight back down the same connection.
// Here the officer asked in words, the assistant worked out what they meant and
// prepared the document while answering -- so the bytes already exist by the time
// anything is clicked, and what the console has is an id beside the reply.
//
// # What limits what it can serve
//
// Everything that limited the document when it was made, because it is the same
// document. Its rows were read inside officeragent.BuildExport by RowsFor, with
// the scope of the officer who asked, out of the closed vocabulary in
// officeragent/fields.go. Nothing is read here at all: this hands over bytes that
// were settled a few minutes ago.
//
// What is decided here is who may have them, and it is decided structurally. The
// store composes its key from the officer's own user id, so an officer who
// somehow comes by somebody else's id looks up a key that was never written and
// is told there is no such export -- the same answer as expired, evicted and
// never existed, and deliberately the same, because distinguishing them would
// tell an officer that a document exists which is not theirs.
func (s *Server) downloadPreparedExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	file := s.preparedExportFor(w, r)
	if file == nil {
		return
	}

	// Recorded before a byte goes out, like the export next door, and with what
	// an auditor would want about a file that has left the building: who, which
	// document, which format, how many rows. The second record this document
	// has: officer.agent.export_prepared was written when it was produced.
	s.Audit.Record(ctx, audit.Event{
		Action: "officer.agent.export_download", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "officer_agent",
		ResourceID:   file.ID,
		Detail: map[string]any{
			"format": file.Format,
			"rows":   file.Rows,
			"bytes":  len(file.Body),
		},
	}.FromContext(ctx))

	w.Header().Set("Content-Type", file.ContentType)
	// The same handling the export route's filename gets, for the same reason: a
	// name built from what somebody typed, written into a header. See
	// contentDisposition in officer_report_export.go.
	w.Header().Set("Content-Disposition", contentDisposition(file.Filename))
	w.Header().Set("Content-Length", strconv.Itoa(len(file.Body)))
	// What the officer is holding, for a console that wants to say so.
	w.Header().Set("X-Esurvey-Rows", strconv.Itoa(file.Rows))
	w.Header().Set("X-Esurvey-Format", file.Format)

	if _, err := w.Write(file.Body); err != nil {
		// The headers have gone; there is no status left to change. Recorded and
		// dropped, which is what a truncated download looks like to the officer.
		s.Log.Warn("writing a prepared export failed",
			"error", err, "format", file.Format)
	}
}

// preparedExportFor answers everything about a download that is settled before a
// byte is written, and writes the refusal itself where there is one.
//
// Separate from the handler above so that the refusals can be tested without an
// audit recorder behind them -- the same seam officerVoice has, for the same
// reason.
func (s *Server) preparedExportFor(
	w http.ResponseWriter, r *http.Request,
) *officeragent.PreparedFile {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in first")
		return nil
	}
	// A farmer holds report:read for their own claims, so the route guard alone
	// does not keep a handset out. Refused for the reason the export route
	// refuses one: this is an office document, and nothing here prepares one for
	// a farmer in the first place.
	if p.FarmerID != "" {
		writeError(w, http.StatusForbidden, "OFFICER_ONLY",
			"a farmer's own claim is printed from the claim itself")
		return nil
	}
	if s.PreparedExports == nil {
		// Nothing can have been prepared, so nothing can be waiting. Said as
		// unavailability rather than as a 404, because the officer's console may
		// be offering a button that this deployment cannot honour.
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE",
			"prepared downloads are not available on this deployment")
		return nil
	}

	file, err := s.PreparedExports.Fetch(ctx, p.UserID, r.PathValue("id"))
	if err != nil {
		if errors.Is(err, officeragent.ErrNoPreparedExport) {
			writeError(w, http.StatusNotFound, "NO_PREPARED_EXPORT",
				"that prepared download is no longer available; ask the "+
					"assistant for it again")
			return nil
		}
		s.Log.Warn("reading a prepared export failed", "error", err)
		writeError(w, http.StatusInternalServerError, "EXPORT_FAILED",
			"the prepared download could not be read")
		return nil
	}
	return file
}
