package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/retention"
)

// listRetentionPolicies publishes what the platform keeps and for how long.
//
// Readable by more people than can run it, deliberately. "How long do you
// hold my photographs" is a question a farmer, an officer and an auditor can
// all reasonably ask, and an answer that requires database access is not an
// answer anyone will get.
func (s *Server) listRetentionPolicies(w http.ResponseWriter, r *http.Request) {
	if s.Retention == nil {
		writeError(w, http.StatusServiceUnavailable, "RETENTION_UNAVAILABLE",
			"retention is not configured on this deployment")
		return
	}

	policies, err := s.Retention.Policies(r.Context())
	if err != nil {
		s.Log.Error("reading retention policies", "error", err)
		writeError(w, http.StatusInternalServerError, "RETENTION_FAILED",
			"the request could not be completed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": policies})
}

type sweepRequest struct {
	Subject string `json:"subject"`
	// Confirm is the second ask. Absent, the sweep counts what it would touch
	// and writes nothing.
	Confirm bool `json:"confirm"`
}

// runRetention applies one policy, or reports what it would do.
//
// The only irreversible operation this API exposes. It needs three things to
// agree before it writes: the policy enabled in the database, confirm in the
// request, and an administrator's permission on the route. Any one of them
// missing produces a count and no change.
func (s *Server) runRetention(w http.ResponseWriter, r *http.Request) {
	if s.Retention == nil {
		writeError(w, http.StatusServiceUnavailable, "RETENTION_UNAVAILABLE",
			"retention is not configured on this deployment")
		return
	}

	var req sweepRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.Subject == "" {
		writeError(w, http.StatusBadRequest, "SUBJECT_REQUIRED",
			"name the subject to sweep")
		return
	}

	res, err := s.Retention.Sweep(r.Context(), req.Subject, req.Confirm)
	if err != nil {
		if errors.Is(err, retention.ErrNotEnabled) {
			writeError(w, http.StatusConflict, "RETENTION_NOT_ENABLED",
				"This policy is not enabled. Enabling it is a separate, "+
					"recorded decision — nothing is deleted until it is.")
			return
		}
		s.Log.Error("retention sweep", "subject", req.Subject, "error", err)
		writeError(w, http.StatusInternalServerError, "RETENTION_FAILED",
			"the sweep could not be completed")
		return
	}

	// Recorded whichever way it went. A dry run is a question somebody asked
	// about a lot of people's data, and that is worth a row too.
	s.Audit.Record(r.Context(), audit.Event{
		Action:       "retention.swept",
		Outcome:      audit.OutcomeAllow,
		ResourceType: "retention_policy",
		ResourceID:   req.Subject,
		Detail: map[string]any{
			"affected": res.Affected,
			"dry_run":  res.DryRun,
			"cutoff":   res.Cutoff,
			"action":   res.Action,
		},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, res)
}

var _ = rbac.PermRetentionRun
