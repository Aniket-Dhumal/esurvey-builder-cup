package api

import (
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The cross-district view.
//
// Gated on assessment:read rather than on a role. A State Officer is the
// intended reader, and a Collector calling it gets their own district broken
// down by taluka, which is a working comparison and costs nothing to allow.
// The scope predicate decides what comes back, as everywhere else.
func (s *Server) statewide(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	// A farmer holds report:read, and their scope is one registry identity
	// rather than a jurisdiction -- so the query would answer them with an
	// empty page rather than a refusal, which reads as a broken screen. Said
	// plainly instead.
	if p.IsFarmer() {
		writeError(w, http.StatusForbidden, "NOT_AN_OVERSIGHT_VIEW",
			"this view is for officers with a jurisdiction; your own claims "+
				"are on your reports page")
		return
	}

	view, err := s.Reports.StatewideView(ctx, p.Scope(), r.URL.Query().Get("event_id"))
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
