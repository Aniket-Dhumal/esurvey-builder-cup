package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// The appeal path.
//
// A farmer who disagrees with the figure their report was decided at now has
// somewhere to go. Every route here is scoped like the report routes: a
// farmer reaches their own, an officer the ones in their villages.

type fileGrievanceRequest struct {
	ReportID       string  `json:"report_id"`
	Ground         string  `json:"ground"`
	Statement      string  `json:"statement"`
	ClaimedPercent float64 `json:"claimed_percent"`
	// Channel records how it arrived. A grievance taken at a counter by an
	// officer is still the farmer's grievance, and the audit trail should
	// say which it was.
	Channel string `json:"channel"`
}

func (s *Server) fileGrievance(w http.ResponseWriter, r *http.Request) {
	principal := rbac.FromContext(r.Context())

	var req fileGrievanceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	in := report.NewGrievance{
		ReportID:  req.ReportID,
		Ground:    report.GrievanceGround(req.Ground),
		Statement: req.Statement,
		Channel:   req.Channel,
		FiledBy:   principal.UserID,
	}
	// Zero is a real claim -- "nothing was assessed and something was lost"
	// is a grievance -- so an omitted figure has to be distinguishable from
	// a claimed zero. Absent means the farmer did not name one.
	if req.ClaimedPercent > 0 {
		p := req.ClaimedPercent
		in.ClaimedPercent = &p
	}

	g, err := s.Reports.FileGrievance(r.Context(), in, principal.Scope())
	if err != nil {
		s.writeGrievanceError(w, err)
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		Action:       "grievance.filed",
		Outcome:      audit.OutcomeAllow,
		ResourceType: "grievance",
		ResourceID:   g.ID,
		Detail: map[string]any{
			"reference": g.Reference,
			"report":    g.ReportReference,
			"ground":    string(g.Ground),
			"channel":   g.FiledChannel,
		},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) listGrievances(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	result, err := s.Reports.Grievances(r.Context(), report.GrievanceFilter{
		Scope:    rbac.FromContext(r.Context()).Scope(),
		ReportID: q.Get("report_id"),
		State:    q.Get("state"),
		OpenOnly: q.Get("open") == "true",
		Page:     pg,
	})
	if err != nil {
		s.writeGrievanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getGrievance(w http.ResponseWriter, r *http.Request) {
	g, err := s.Reports.Grievance(
		r.Context(), r.PathValue("grievanceID"),
		rbac.FromContext(r.Context()).Scope())
	if err != nil {
		s.writeGrievanceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

type assignGrievanceRequest struct {
	OfficerID string `json:"officer_id"`
}

func (s *Server) assignGrievance(w http.ResponseWriter, r *http.Request) {
	principal := rbac.FromContext(r.Context())

	var req assignGrievanceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}
	if req.OfficerID == "" {
		// Assigning to yourself is the ordinary case for a Tehsildar picking
		// one up, so an omitted officer means "me" rather than an error.
		req.OfficerID = principal.UserID
	}

	// The listing scope is what establishes the caller may touch this one.
	current, err := s.Reports.Grievance(
		r.Context(), r.PathValue("grievanceID"), principal.Scope())
	if err != nil {
		s.writeGrievanceError(w, err)
		return
	}

	// The officer it goes to must be posted over the claim's village. Any id
	// was accepted, so an appeal could be handed to an officer in another
	// district or state -- who could then not even open it, and the farmer's
	// appeal would sit with nobody able to answer.
	if req.OfficerID != principal.UserID {
		assignee, err := s.Identity.ByID(r.Context(), req.OfficerID)
		ok := err == nil && assignee.Kind == rbac.KindOfficer && assignee.Active()
		if ok {
			var ids []string
			for _, grant := range assignee.Grants {
				if grant.JurisdictionID != "" {
					ids = append(ids, grant.JurisdictionID)
				}
			}
			in, err := s.Identity.VillagesInScope(r.Context(),
				[]string{current.VillageCode}, ids)
			ok = err == nil && in[current.VillageCode]
		}
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "ASSIGNEE_OUT_OF_AREA",
				"that officer is not posted over this claim's village")
			return
		}
	}

	g, err := s.Reports.AssignGrievance(
		r.Context(), r.PathValue("grievanceID"), req.OfficerID)
	if err != nil {
		s.writeGrievanceError(w, err)
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		Action:       "grievance.assigned",
		Outcome:      audit.OutcomeAllow,
		ResourceType: "grievance",
		ResourceID:   g.ID,
		Detail: map[string]any{
			"reference":      g.Reference,
			"assigned_to":    g.AssignedTo,
			"escalated_past": g.EscalatedPast,
		},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, g)
}

type decideGrievanceRequest struct {
	State          string   `json:"state"`
	Reason         string   `json:"reason"`
	RevisedPercent *float64 `json:"revised_percent"`

	// Optional. Most appeals are about the percentage, and one that says
	// nothing about area leaves it where the original decision put it.
	RevisedAreaHa *float64 `json:"revised_area_ha"`
}

func (s *Server) decideGrievance(w http.ResponseWriter, r *http.Request) {
	principal := rbac.FromContext(r.Context())

	var req decideGrievanceRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", err.Error())
		return
	}

	if _, err := s.Reports.Grievance(
		r.Context(), r.PathValue("grievanceID"), principal.Scope()); err != nil {
		s.writeGrievanceError(w, err)
		return
	}

	g, err := s.Reports.DecideGrievance(r.Context(), report.GrievanceDecision{
		GrievanceID:    r.PathValue("grievanceID"),
		State:          report.GrievanceState(req.State),
		Reason:         req.Reason,
		RevisedPercent: req.RevisedPercent,
		RevisedAreaHa:  req.RevisedAreaHa,
		OfficerID:      principal.UserID,
		// Carried onto the superseding adjudication, so the record shows the
		// rank that changed the figure and not only the person.
		Role: principal.PrimaryRole(),
	})
	if err != nil {
		s.writeGrievanceError(w, err)
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		Action:       "grievance.decided",
		Outcome:      audit.OutcomeAllow,
		ResourceType: "grievance",
		ResourceID:   g.ID,
		Detail: map[string]any{
			"reference":        g.Reference,
			"state":            string(g.State),
			"disputed_percent": g.DisputedPercent,
			"revised_percent":  g.RevisedPercent,
		},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, g)
}

func (s *Server) withdrawGrievance(w http.ResponseWriter, r *http.Request) {
	principal := rbac.FromContext(r.Context())

	g, err := s.Reports.WithdrawGrievance(
		r.Context(), r.PathValue("grievanceID"), principal.Scope())
	if err != nil {
		s.writeGrievanceError(w, err)
		return
	}

	s.Audit.Record(r.Context(), audit.Event{
		Action:       "grievance.withdrawn",
		Outcome:      audit.OutcomeAllow,
		ResourceType: "grievance",
		ResourceID:   g.ID,
		Detail:       map[string]any{"reference": g.Reference},
	}.FromContext(r.Context()))

	writeJSON(w, http.StatusOK, g)
}

// grievanceGrounds publishes the choices a farmer picks from, so the app does
// not carry its own copy of a list the server validates against.
func (s *Server) grievanceGrounds(w http.ResponseWriter, r *http.Request) {
	type ground struct {
		Code  string `json:"code"`
		Label string `json:"label"`
	}
	// Ordered, because a map is not. The order is the order a farmer is most
	// likely to want, not alphabetical.
	order := []report.GrievanceGround{
		report.GroundDamagePercentLow,
		report.GroundAreaWrong,
		report.GroundVisitNeeded,
		report.GroundEvidenceIgnored,
		report.GroundOther,
	}
	out := make([]ground, 0, len(order))
	for _, g := range order {
		out = append(out, ground{Code: string(g), Label: report.GroundLabels[g]})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"grounds":     out,
		"sla_days":    int(report.GrievanceSLA.Hours() / 24),
		"sla_message": "An officer must answer within 15 days.",
	})
}

func (s *Server) writeGrievanceError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, report.ErrGrievanceNotFound), errors.Is(err, report.ErrNotFound):
		writeError(w, http.StatusNotFound, "GRIEVANCE_NOT_FOUND",
			"no such grievance")
	case errors.Is(err, report.ErrNotAppealable):
		writeError(w, http.StatusConflict, "NOT_APPEALABLE", err.Error())
	case errors.Is(err, report.ErrAlreadyOpen):
		writeError(w, http.StatusConflict, "GRIEVANCE_ALREADY_OPEN",
			"A grievance on this report is already open. It will be answered "+
				"before another can be filed.")
	case errors.Is(err, report.ErrSelfReview):
		// Worth its own code and its own sentence: this is the rule that
		// makes the appeal an appeal, and an officer who hits it should
		// understand it is a design decision rather than a bug.
		writeError(w, http.StatusForbidden, "SELF_REVIEW",
			"An appeal cannot be answered by the officer whose decision is "+
				"being appealed. Assign it to someone else.")
	case errors.Is(err, report.ErrGrievanceState), errors.Is(err, report.ErrBadState):
		writeError(w, http.StatusUnprocessableEntity, "GRIEVANCE_STATE", err.Error())
	default:
		s.Log.Error("grievance", "error", err)
		writeError(w, http.StatusInternalServerError, "GRIEVANCE_FAILED",
			"the request could not be completed")
	}
}
