package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
	"github.com/agribid/esurvey-platform/backend/internal/store"
)

// worklist is what the signed-in officer has been asked to verify.
// overdue lists everything past its deadline across a jurisdiction: the
// village pair's assignments, and the rungs above them.
//
// Separate from the worklist, which filters on the officer's own id and so
// shows the seven-day deadline only to the person breaching it. An officer
// who has stopped opening the console is precisely the one whose reports are
// late, and until this there was no way for anybody above them to find out.
//
// Requires the review permission rather than verify: this is a supervisor's
// view of other people's work, and a Talathi has no business reading it.
func (s *Server) overdue(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	result, err := s.Reports.Overdue(r.Context(), p.Scope(), pg)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// The other half of "what is late".
	//
	// Two different shapes of lateness reach one screen on purpose. Below,
	// a named officer has missed their seven days on a field visit. Above,
	// a *rung* has: the report is waiting at the circle or the tehsil and
	// nobody is assigned to it, because above the village pair a report is
	// not allocated to a person -- whoever holds the posting owes the
	// answer.
	//
	// An officer asking what is stuck does not care which kind it is, and
	// making them read two screens to find out is how the second one never
	// gets opened.
	rungs, err := s.Reports.OverdueRungs(r.Context(), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"assignments": result,
		"rungs":       rungs,
	})
}

func (s *Server) worklist(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())
	includeDone := r.URL.Query().Get("include_completed") == "true"

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	result, overdue, err := s.Reports.Worklist(r.Context(), p.UserID, includeDone, pg)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, struct {
		page.Result[report.WorklistEntry]
		// Across the whole list, not the page. An officer looking at page one
		// needs to know there are nine overdue on page four.
		Overdue int `json:"overdue"`
	}{result, overdue})
}

// villageAuthorities answers who is allocated to a village.
//
// Published because the gap it exposes is real: a village with no Talathi or
// Krushi Sahayak posted cannot have a report verified, and that should be
// visible on an administration screen rather than discovered when a report
// sits unassigned.
func (s *Server) villageAuthorities(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("villageCode")
	p := rbac.FromContext(r.Context())

	// Who is posted to a village is officer information. It answered for any
	// village in any state, and to a farmer as well -- whose report:read is
	// for their own claims, not the names of the officers deciding them.
	if p.IsFarmer() {
		writeError(w, http.StatusForbidden, "FORBIDDEN",
			"officer postings are not available to a farmer")
		return
	}
	in, err := s.Identity.VillagesInScope(r.Context(), []string{code},
		p.Scope().JurisdictionIDs)
	if err != nil {
		s.Log.Error("scoping village authorities", "error", err)
		writeError(w, http.StatusInternalServerError, "SCOPE_FAILED", "")
		return
	}
	if !in[code] {
		writeError(w, http.StatusNotFound, "VILLAGE_NOT_FOUND",
			"no village with that code in your area")
		return
	}

	auth, err := s.Reports.Authorities(r.Context(), code)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	if auth == nil {
		auth = []report.Authority{}
	}

	have := map[rbac.Role]bool{}
	for _, a := range auth {
		have[a.Role] = true
	}
	var missing []string
	for _, want := range []rbac.Role{rbac.RoleTalathi, rbac.RoleKrushiSahayak} {
		if !have[want] {
			missing = append(missing, string(want))
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"village_lgd_code": code,
		"authorities":      auth,
		"missing_roles":    missing,
		"can_verify":       len(missing) == 0,
	})
}

// allocateReport assigns a report to its village's authorities.
func (s *Server) allocateReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	assignments, err := s.Reports.Allocate(ctx, rep.ID, rep.Place.VillageCode, p.UserID)
	if err != nil {
		s.writeVerificationError(w, err)
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report.allocated", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"reference": rep.Reference,
			"assigned":  assignmentSummary(assignments),
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, map[string]any{"assignments": assignments})
}

// verificationBrief is what an assigned officer sees before going to the field.
//
// The report, who else is assigned, and the satellite evidence — crop presence
// and fraud indicators, which are actionable on a visit ("the record says
// soybean sown in June, the curve peaks in October; check what is growing").
//
// It does not carry the peer's finding: the two village officers examine the
// claim independently, and each reads the other only once both have recorded.
//
// # Why it now carries the model's figure, having refused to
//
// It used to withhold the assessment as well, on the reasoning written into
// internal/calibration: an officer anchored to a number produces a finding
// that cannot afterwards be used to judge that number. That reasoning has not
// become wrong. It was overruled deliberately, because the process it assumed
// -- an officer forming an independent figure, which in practice meant going
// to the field -- is not the process this platform is for. The assessment is
// built from the farmer's own clips and the satellite record; asking an
// officer to re-derive it from nothing was asking for a second survey.
//
// So the officer is shown what the panel found and confirms it, or overrides
// it with a figure and a reason. What that costs is recorded rather than
// hidden: every finding made this way is marked saw_model_assessment, and
// internal/calibration reports informed and blind findings separately, so the
// agreement statistic cannot quietly flatter the model. The blind set stops
// growing from here, and what it already holds stays the honest baseline.
func (s *Server) verificationBrief(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	// Officers only. A farmer's report:read is for their own claim, and this
	// answers with the officers' findings -- figures and names -- while the
	// pair is still examining it. The farmer is shown the outcome through
	// /progress once both village officers have recorded, and not before.
	if p.IsFarmer() {
		writeError(w, http.StatusForbidden, "FORBIDDEN",
			"officer findings are shown to the farmer through the claim's progress")
		return
	}

	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	alloc, err := s.Reports.AllocationFor(ctx, rep.ID, p.UserID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	findings, err := s.Reports.Findings(ctx, rep.ID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	mine, peerRecorded := false, false
	for _, f := range findings {
		switch {
		case f.OfficerID == p.UserID:
			mine = true
		case f.Tier == report.TierVillagePair:
			peerRecorded = true
		}
	}

	// Whether a revised finding may be submitted.
	//
	// Both have filed, so each has read the other, and the report is still
	// the pair's — nobody above them is mid-decision on it. Computed here so
	// the screen offers the action exactly when the endpoint would accept it,
	// rather than offering it always and explaining the refusal afterwards.
	mayAmend := mine && peerRecorded &&
		alloc.Assigned && alloc.Tier == report.TierVillagePair &&
		(rep.ApprovalLevel == "" || rep.ApprovalLevel == report.LevelVillagePair) &&
		rep.State != report.StateAdjudicated &&
		rep.State != report.StateClosed &&
		rep.State != report.StateWithdrawn

	out := map[string]any{
		"report":                rep,
		"assigned":              alloc.Assigned,
		"tier":                  alloc.Tier,
		"already_recorded":      mine,
		"peer_recorded":         peerRecorded,
		"peer_finding_withheld": peerRecorded && !mine,
		"may_amend":             mayAmend,
	}

	// Once you have recorded, the blind is over and you see everything.
	if mine || !alloc.Assigned {
		out["findings"] = findings
		out["consensus"] = report.Reconcile(findings)
	}

	// What the panel found, for the officer to confirm or overrule. Absent
	// where no assessment has run yet -- a claim can reach an officer before
	// its evidence has been through the panel -- and the screen then asks for
	// their own figure instead of offering one to agree with.
	if s.Store != nil {
		if a, err := s.Store.LatestForClaim(ctx, rep.ID); err == nil && a != nil {
			out["assessment"] = a
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			// Not fatal: the brief is still worth showing without it, and an
			// officer who cannot see the panel is better off than one who
			// cannot see the claim.
			s.Log.Warn("reading the assessment for a verification brief",
				"report", rep.ID, "error", err)
		}
	}

	writeJSON(w, http.StatusOK, out)
}

type verifyRequest struct {
	Outcome        string     `json:"outcome"`
	DamagePercent  *float64   `json:"damage_percent"`
	AreaAffectedHa *float64   `json:"area_affected_ha"`
	Findings       string     `json:"findings"`
	VisitedAt      *time.Time `json:"visited_at,omitempty"`

	// SawModelAssessment records whether the officer consulted the satellite
	// cross-check before recording. Reported by the client because only it
	// knows whether the panel was opened; it feeds calibration, not authorization.
	SawModelAssessment bool `json:"saw_model_assessment"`

	Items []report.VerifiedItem `json:"items,omitempty"`
}

// verifyReport records one officer's finding.
//
// Only an allocated authority may record. Jurisdiction reach is not enough:
// a Tehsildar can see every report in their taluka and is not the village's
// verifier. Scope says who may look; assignment says who must act.
func (s *Server) verifyReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req verifyRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	alloc, err := s.Reports.AllocationFor(ctx, rep.ID, p.UserID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	if alloc.Assigned && alloc.Completed {
		// Not a refusal of authority -- the opposite. They are the village's
		// verifier and have already been.
		// Final in place, and not final for ever: once the other authority
		// has filed too, either of them may record a revised finding that
		// supersedes their own. Saying so here is the difference between a
		// wall and a next step.
		writeError(w, http.StatusConflict, "ALREADY_RECORDED",
			"you have already recorded a finding on this report. A finding "+
				"cannot be edited — once the other authority has recorded "+
				"theirs, you can read it and submit a revised finding, and "+
				"both are kept")
		return
	}
	if !alloc.Assigned {
		s.Audit.Record(ctx, audit.Event{
			Action: audit.ActionAuthzDenied, Outcome: audit.OutcomeDeny,
			ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
			ResourceType: "crop_loss_report", ResourceID: rep.ID,
			JurisdictionCode: rep.Place.VillageCode,
			Detail: map[string]any{
				"reason":    "not the allocated authority",
				"reference": rep.Reference,
			},
		}.FromContext(ctx))
		writeError(w, http.StatusForbidden, "NOT_ALLOCATED",
			"you are not the allocated authority for this report; only the "+
				"Talathi and Krushi Sahayak posted to "+rep.Place.VillageName+" may verify it")
		return
	}

	// The role recorded on the finding is the one the officer actually holds
	// for this village, not one they name.
	role := verifierRole(p, alloc.Tier)

	finding, err := s.Reports.RecordFinding(ctx, report.NewFinding{
		ReportID:           rep.ID,
		OfficerID:          p.UserID,
		Role:               role,
		Tier:               alloc.Tier,
		Outcome:            report.Outcome(req.Outcome),
		DamagePercent:      req.DamagePercent,
		AreaAffectedHa:     req.AreaAffectedHa,
		Findings:           req.Findings,
		VisitedAt:          req.VisitedAt,
		SawModelAssessment: req.SawModelAssessment,
		Items:              req.Items,
	})
	if err != nil {
		s.writeVerificationError(w, err)
		return
	}

	findings, err := s.Reports.Findings(ctx, rep.ID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	consensus := report.Reconcile(findings)

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report.verified", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"reference":        rep.Reference,
			"tier":             string(alloc.Tier),
			"role":             string(role),
			"outcome":          req.Outcome,
			"damage_percent":   req.DamagePercent,
			"saw_peer_finding": finding.SawPeerFinding,
			"consensus_route":  string(consensus.Route),
		},
	}.FromContext(ctx))

	out := map[string]any{"finding": finding, "consensus": consensus}
	if consensus.Complete {
		out["findings"] = findings
	}
	writeJSON(w, http.StatusCreated, out)
}

// findings returns the recorded verifications, once the caller may see them.
func (s *Server) reportFindings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	// Officers only. A farmer's report:read is for their own claim, and this
	// answers with the officers' findings -- figures and names -- while the
	// pair is still examining it. The farmer is shown the outcome through
	// /progress once both village officers have recorded, and not before.
	if p.IsFarmer() {
		writeError(w, http.StatusForbidden, "FORBIDDEN",
			"officer findings are shown to the farmer through the claim's progress")
		return
	}

	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	findings, err := s.Reports.Findings(ctx, rep.ID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// The blind holds only against the other member of the pair. A
	// supervisor, and an officer who has already recorded, see everything.
	alloc, err := s.Reports.AllocationFor(ctx, rep.ID, p.UserID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	if alloc.Assigned {
		mine := false
		for _, f := range findings {
			if f.OfficerID == p.UserID {
				mine = true
			}
		}
		if !mine {
			writeError(w, http.StatusForbidden, "BLIND_UNTIL_RECORDED",
				"record your own finding first; the pair verify independently")
			return
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"findings":  findings,
		"consensus": report.Reconcile(findings),
	})
}

type adjudicateRequest struct {
	AdjudicatedPercent float64  `json:"adjudicated_percent"`
	AdjudicatedAreaHa  float64  `json:"adjudicated_area_ha"`
	Reason             string   `json:"reason"`
	ShownModelPercent  *float64 `json:"shown_model_percent,omitempty"`
}

// adjudicateReport records the final figure.
//
// The route decides who may: consensus and arbitration are the Circle
// Officer's, escalation is the Tehsildar's. A District Magistrate may take any
// of them, which is what having a Collector is for.
func (s *Server) adjudicateReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req adjudicateRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	rep, err := s.Reports.Get(ctx, r.PathValue("reportID"), p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	findings, err := s.Reports.Findings(ctx, rep.ID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	consensus := report.Reconcile(findings)
	if !consensus.Complete {
		writeError(w, http.StatusConflict, "PAIR_INCOMPLETE",
			"both village authorities must record a finding before this can be adjudicated")
		return
	}

	// The village pair never adjudicate their own verification. That is the
	// whole point of routing it upward.
	role := p.PrimaryRole()
	switch {
	case p.HasRole(rbac.RoleDistrictMagistrate):
		role = rbac.RoleDistrictMagistrate
	case consensus.Route == report.RouteEscalation && p.HasRole(rbac.RoleTehsildar):
		role = rbac.RoleTehsildar
	case consensus.Route != report.RouteEscalation && p.HasRole(rbac.RoleMandalOfficer):
		role = rbac.RoleMandalOfficer
	case consensus.Route != report.RouteEscalation && p.HasRole(rbac.RoleTehsildar):
		// No Circle Officer is posted -- most talukas have no circles mapped
		// -- so the Tehsildar absorbs the arbitration rather than the report
		// stalling.
		role = rbac.RoleTehsildar
	default:
		writeError(w, http.StatusForbidden, "NOT_THE_ADJUDICATOR",
			"this report routes to the "+string(consensus.AdjudicatedBy)+
				"; "+consensus.Explanation)
		return
	}

	adj, err := s.Reports.Adjudicate(ctx, report.NewAdjudication{
		ReportID:           rep.ID,
		OfficerID:          p.UserID,
		Role:               role,
		AdjudicatedPercent: req.AdjudicatedPercent,
		AdjudicatedAreaHa:  req.AdjudicatedAreaHa,
		Reason:             req.Reason,
		ShownModelPercent:  req.ShownModelPercent,
	})
	if err != nil {
		s.writeVerificationError(w, err)
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report.adjudicated", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"reference":           rep.Reference,
			"route":               string(adj.Route),
			"divergence":          adj.Divergence,
			"shown_first":         adj.ShownFirstPercent,
			"shown_second":        adj.ShownSecondPercent,
			"adjudicated_percent": adj.AdjudicatedPercent,
			"role":                string(role),
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusCreated, map[string]any{
		"adjudication": adj,
		"consensus":    consensus,
	})
}

func assignmentSummary(a []report.Assignment) []map[string]string {
	out := make([]map[string]string, 0, len(a))
	for _, x := range a {
		out = append(out, map[string]string{
			"role": string(x.Role), "officer": x.OfficerName, "tier": string(x.Tier),
		})
	}
	return out
}

func (s *Server) writeVerificationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, report.ErrNoAuthority):
		// 422 rather than 500: the request was well formed and the
		// administration has a gap. It needs an officer posted, not a retry.
		writeError(w, http.StatusUnprocessableEntity, "NO_VILLAGE_AUTHORITY", err.Error())
	case errors.Is(err, report.ErrNotAssigned):
		writeError(w, http.StatusForbidden, "NOT_ALLOCATED", err.Error())
	case errors.Is(err, report.ErrAlreadyRecorded):
		writeError(w, http.StatusConflict, "ALREADY_RECORDED",
			"you have already recorded a finding; a verification is final once submitted")
	case errors.Is(err, report.ErrPairIncomplete):
		writeError(w, http.StatusConflict, "PAIR_INCOMPLETE", err.Error())
	default:
		s.writeReportError(w, err)
	}
}
