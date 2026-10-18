package api

import (
	"net/http"
	"strconv"

	"github.com/agribid/esurvey-platform/backend/internal/calibration"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// How the three figures compare.
//
// The farmer's declaration, the panel's assessment and the officer's finding
// have been joinable since the schema was written, and nothing has ever read
// them. This is the endpoint that does -- and it is worth having now, while it
// says almost nothing, because in a season it becomes the only calibration
// set this platform will get for free.

func (s *Server) calibration(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	if s.Calibration == nil {
		writeError(w, http.StatusNotImplemented, "NO_CALIBRATION",
			"calibration is not enabled on this deployment")
		return
	}

	limit := 1000
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}

	samples, err := s.Calibration.Samples(ctx, p.Scope(), limit)
	if err != nil {
		s.Log.Error("reading calibration samples", "error", err)
		writeError(w, http.StatusInternalServerError, "CALIBRATION_FAILED",
			"the comparison could not be read")
		return
	}

	summary := calibration.Summarise(samples)

	// The samples themselves only when asked for. The summary is what anybody
	// should read first, and a thousand rows of three percentages is a table
	// nobody scrolls.
	out := map[string]any{"summary": summary}
	if r.URL.Query().Get("samples") == "true" {
		out["samples"] = samples
	}
	writeJSON(w, http.StatusOK, out)
}
