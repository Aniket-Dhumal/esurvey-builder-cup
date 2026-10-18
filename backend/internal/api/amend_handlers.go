package api

import (
	"errors"
	"net/http"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// Revising a finding after reading the other authority's.
//
// The pair record blind and then read each other. Until now that was where it
// ended: whatever gap the two numbers had, they kept, and a gap of a few
// points went to the Circle Officer as a disagreement to arbitrate. Two people
// who stood in the same field can usually close a gap like that between
// themselves, and the platform gave them no way to.
//
// So this exists, under narrow conditions: only the village pair, only after
// both have filed, only while the report is still theirs, and never in place
// -- the earlier finding is superseded and kept, marked as an amendment rather
// than as a return. See internal/report/amend.go for the reasoning in full.

// amendVerification replaces the caller's own standing finding.
func (s *Server) amendVerification(w http.ResponseWriter, r *http.Request) {
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
	if !alloc.Assigned {
		writeError(w, http.StatusForbidden, "NOT_ALLOCATED",
			"you are not the allocated authority for this report")
		return
	}
	if alloc.Tier != report.TierVillagePair {
		writeError(w, http.StatusForbidden, "NOT_THE_PAIR",
			"only the village pair amend; a level above records afresh at its own tier")
		return
	}

	finding, err := s.Reports.AmendFinding(ctx, report.NewFinding{
		ReportID:           rep.ID,
		OfficerID:          p.UserID,
		Role:               verifierRole(p, alloc.Tier),
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
		s.writeAmendError(w, err)
		return
	}

	findings, err := s.Reports.Findings(ctx, rep.ID)
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	consensus := report.Reconcile(findings)

	// Recorded as its own action, not as another report.verified. An audit
	// asking how often officers move after seeing each other has to be able
	// to count these, and an audit asking who verified this claim has to not
	// count them twice.
	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report.finding_amended", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"reference":       rep.Reference,
			"outcome":         req.Outcome,
			"damage_percent":  req.DamagePercent,
			"consensus_route": string(consensus.Route),
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, map[string]any{
		"finding":   finding,
		"findings":  findings,
		"consensus": consensus,
	})
}

// verifierRole is the role a finding is recorded under.
//
// The one the officer actually holds for this village, not one they name. An
// officer who holds both -- which happens where a village is short-staffed and
// one person carries two postings -- records as Talathi, because that is the
// revenue authority and the one the pair is defined by.
func verifierRole(p *rbac.Principal, tier report.Tier) rbac.Role {
	switch {
	case p.HasRole(rbac.RoleKrushiSahayak) && !p.HasRole(rbac.RoleTalathi):
		return rbac.RoleKrushiSahayak
	case p.HasRole(rbac.RoleMandalOfficer) && tier == report.TierArbitration:
		return rbac.RoleMandalOfficer
	case p.HasRole(rbac.RoleTehsildar) && tier == report.TierEscalation:
		return rbac.RoleTehsildar
	}
	return rbac.RoleTalathi
}

func (s *Server) writeAmendError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, report.ErrNothingToAmend):
		writeError(w, http.StatusConflict, "NOTHING_TO_AMEND",
			"you have not recorded a finding on this report yet")
	case errors.Is(err, report.ErrPeerHasNotRecorded):
		// 409 and not 403: nothing is wrong with the caller, the moment has
		// not arrived. An amendment before the other authority has filed
		// would be a rewrite of a blind finding, which is the one thing the
		// pair's independence rests on.
		writeError(w, http.StatusConflict, "PEER_NOT_RECORDED",
			"the other village authority has not recorded yet; a finding can "+
				"only be revised after you have read theirs")
	case errors.Is(err, report.ErrAmendWindowClosed):
		writeError(w, http.StatusConflict, "AMEND_WINDOW_CLOSED",
			"this report has left the village pair — "+err.Error()+". If it "+
				"needs another look, the level holding it can send it back")
	default:
		s.writeVerificationError(w, err)
	}
}
