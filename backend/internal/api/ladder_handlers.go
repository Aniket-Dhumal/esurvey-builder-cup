package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// The approval ladder over HTTP.
//
// Every rule these handlers appear to enforce is actually enforced below them:
// the order and the contested rule in report.Check, the jurisdiction in the
// store's own query. That is deliberate. A check written here would have to be
// written again in the bulk path and again in whatever endpoint is added next,
// and the third one would be the one somebody forgets.
//
// So these decode, delegate, translate the refusal into something an officer
// can read, and record what happened.

type decideRequest struct {
	// Percent and AreaHa revise the figure. Omitted keeps what the officer was
	// shown, which is the ordinary approval.
	Percent *float64 `json:"damage_percent,omitempty"`
	AreaHa  *float64 `json:"area_affected_ha,omitempty"`

	Reason string `json:"reason,omitempty"`
}

type bulkApproveRequest struct {
	ReportIDs []string `json:"report_ids"`
	Reason    string   `json:"reason,omitempty"`
}

// approveReport records one rung's sign-off and moves the report up.
func (s *Server) approveReport(w http.ResponseWriter, r *http.Request) {
	s.decideOnReport(w, r, false)
}

// returnReport sends a report back one rung, with a reason.
func (s *Server) returnReport(w http.ResponseWriter, r *http.Request) {
	s.decideOnReport(w, r, true)
}

func (s *Server) decideOnReport(w http.ResponseWriter, r *http.Request, back bool) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req decideRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	in := report.Decide{
		ReportID:  r.PathValue("reportID"),
		OfficerID: p.UserID,
		Role:      p.PrimaryRole(),
		Scope:     p.Scope(),
		Percent:   req.Percent,
		AreaHa:    req.AreaHa,
		Reason:    req.Reason,
	}

	var (
		row *report.Approval
		err error
	)
	if back {
		row, err = s.Reports.Return(ctx, in)
	} else {
		row, err = s.Reports.Approve(ctx, in)
	}
	if err != nil {
		s.writeLadderError(w, err)
		return
	}

	action := audit.ActionReportApproved
	if back {
		action = audit.ActionReportReturned
	}
	s.Audit.Record(ctx, audit.Event{
		Action: action, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: in.ReportID,
		Detail: map[string]any{
			"level":          string(row.Level),
			"decision":       row.Decision,
			"percent_before": row.PercentBefore,
			"percent_after":  row.PercentAfter,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, row)
}

// bulkApproveReports approves many reports and says what happened to each.
//
// Partial success is the expected outcome, not an edge case: a Tehsildar
// sweeping a taluka will routinely have a handful of contested reports in the
// selection, and those are exactly the ones that must fall out. So this
// answers 200 with a per-item verdict rather than failing the request.
func (s *Server) bulkApproveReports(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req bulkApproveRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	outcomes, err := s.Reports.BulkApprove(ctx, req.ReportIDs, report.Decide{
		OfficerID: p.UserID,
		Role:      p.PrimaryRole(),
		Scope:     p.Scope(),
		Reason:    req.Reason,
	})
	if err != nil {
		s.writeLadderError(w, err)
		return
	}

	approved := 0
	for _, o := range outcomes {
		if o.Approved {
			approved++
		}
	}

	s.Audit.Record(ctx, audit.Event{
		Action: audit.ActionReportApproved, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report",
		Detail: map[string]any{
			"bulk":     true,
			"selected": len(req.ReportIDs),
			"approved": approved,
			"refused":  len(outcomes) - approved,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, map[string]any{
		"selected": len(req.ReportIDs),
		"approved": approved,
		"refused":  len(outcomes) - approved,
		"results":  outcomes,
	})
}

// reportApprovals returns a report's ladder history.
//
// Including superseded rungs. A report that went up at 40%, came back and went
// up again at 65% has two Circle approvals, and hiding the first would leave
// the record reading as though the Circle always thought 65%.
func (s *Server) reportApprovals(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	// Read through the scoped getter first, so a report outside the officer's
	// posting is not there to have a history read from it.
	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	rows, err := s.Reports.Approvals(ctx, rep.ID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": rows})
}

// writeLadderError turns a refusal into a status and a code.
//
// Every one of these is a sentence an officer reads after pressing a button
// that did nothing, so the message the ladder wrote is passed through: it
// already says where the report actually is, which is the thing they need.
func (s *Server) writeLadderError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, report.ErrNotFound):
		writeError(w, http.StatusNotFound, "REPORT_NOT_FOUND",
			"no crop loss report with that id")

	case errors.Is(err, report.ErrNoRung):
		writeError(w, http.StatusForbidden, "NOT_AN_APPROVER", err.Error())

	case errors.Is(err, report.ErrNotYourRung):
		writeError(w, http.StatusConflict, "NOT_YOUR_LEVEL", err.Error())

	case errors.Is(err, report.ErrPairNotComplete):
		writeError(w, http.StatusConflict, "PAIR_INCOMPLETE", err.Error())

	case errors.Is(err, report.ErrAlreadyClosed):
		writeError(w, http.StatusConflict, "REPORT_CLOSED", err.Error())

	case errors.Is(err, report.ErrContestedBulk):
		writeError(w, http.StatusConflict, "CONTESTED_NEEDS_READING", err.Error())

	case errors.Is(err, report.ErrContestedReason),
		errors.Is(err, report.ErrReasonRequired):
		writeError(w, http.StatusUnprocessableEntity, "REASON_REQUIRED", err.Error())

	case errors.Is(err, report.ErrBadInput):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())

	default:
		s.writeReportError(w, err)
	}
}
