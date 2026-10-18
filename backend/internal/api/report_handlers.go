package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/agristack"
	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

type createReportRequest struct {
	SurveyEventID string `json:"survey_event_id"`

	// FarmerID is the registry identifier. Name, address and handset are
	// filled from the registry rather than the body where they are absent —
	// a report should not be able to state a different name for the same
	// farmer id than the record does.
	FarmerID      string `json:"farmer_id"`
	FarmerName    string `json:"farmer_name,omitempty"`
	FarmerAddress string `json:"farmer_address,omitempty"`
	FarmerMobile  string `json:"farmer_mobile,omitempty"`
	FarmerEmail   string `json:"farmer_email,omitempty"`

	// VillageCode may be omitted when the lines name registry parcels, in
	// which case it is taken from the first of them.
	VillageCode string `json:"village_lgd_code,omitempty"`

	Season   string `json:"season"`
	CropYear string `json:"crop_year"`

	LossCategory string    `json:"loss_category"`
	CalamityType string    `json:"calamity_type"`
	OccurredAt   time.Time `json:"occurred_at"`

	ReportedChannel string `json:"reported_channel,omitempty"`
	LateReason      string `json:"late_reason,omitempty"`

	// FiledAt is when the farmer completed the form, which for the handset is
	// not when this request arrived -- the form is filled in a field with no
	// signal and the queue delivers it later. The intimation window is
	// measured on it where it is given and possible; see report.filingInstant,
	// which decides what to do with a handset clock that disagrees.
	//
	// Omitted by a counter filing, where the arrival is the filing.
	FiledAt *time.Time `json:"filed_at,omitempty"`

	// ExpectedCaptures is how many photographs and clips will follow this
	// claim. It is what lets the office know the claim is complete and start
	// assessing it without an officer asking; see report.New.
	ExpectedCaptures *int `json:"expected_captures,omitempty"`

	DeclarationAccepted bool     `json:"declaration_accepted"`
	DocumentsPromised   []string `json:"documents_promised,omitempty"`
	FarmerSignatureKind string   `json:"farmer_signature_kind,omitempty"`

	Items []report.Item `json:"items"`
}

func (s *Server) createReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req createReportRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	// A farmer files for themselves, or for a farmer they have confirmed on
	// this handset and hold a grant for. Anything else is refused rather than
	// silently rewritten: one mobile number may now act for several farmers,
	// and quietly filing the second one's claim against the first would put
	// the evidence on the wrong person's land. See actingFarmer.
	if p.IsFarmer() {
		acting, err := s.actingFarmer(r, p, req.FarmerID)
		if err != nil {
			writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
			return
		}
		req.FarmerID = acting
	}
	if req.FarmerID == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD", "farmer_id is required")
		return
	}

	rec, err := s.Registry.FarmerRecord(ctx, req.FarmerID)
	if err != nil {
		s.writeFarmerRecordError(w, err)
		return
	}

	// Registry wins on identity.
	req.FarmerName = rec.Farmer.FarmerName
	if req.FarmerMobile == "" {
		req.FarmerMobile = rec.Farmer.MobileNo
	}
	if req.FarmerAddress == "" {
		req.FarmerAddress = rec.Farmer.Address
	}

	villageCode := req.VillageCode
	if villageCode == "" {
		villageCode = rec.Farmer.VillageLGDCode
	}

	// Fill each line's village and parcel from the registry where the line
	// names a farm_id, and check the parcel really belongs to this farmer.
	for i := range req.Items {
		if req.Items[i].FarmID == "" {
			continue
		}
		parcel, ok := rec.ParcelByFarmID(req.Items[i].FarmID)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "PARCEL_NOT_OWNED",
				"line "+strconv.Itoa(i+1)+" names a parcel that does not belong to this farmer")
			return
		}
		if req.Items[i].SurveyNumber == "" {
			req.Items[i].SurveyNumber = parcel.SurveyNumber
		}
		if req.VillageCode == "" {
			villageCode = parcel.VillageLGDCode
		}
	}

	// The officer filing must be posted where the land is. A farmer's own
	// report needs no jurisdiction check — their reach is their records.
	if !p.IsFarmer() {
		if err := s.assertInScope(r, p, villageCode, "crop_loss_report", req.FarmerID); err != nil {
			s.writeScopeError(w, err)
			return
		}
	}

	// Does the registry think this farmer is growing what the event covers?
	//
	// A discrepancy, never a refusal. Computed here because this is the one
	// place that holds both the event and the registry record, and recorded on
	// the report so the village pair meet it as something to settle rather
	// than as a surprise.
	cropMismatch, registryCrop := s.checkEventCrop(ctx, req.SurveyEventID, rec, req.Items)

	rep, err := s.Reports.Create(ctx, report.New{
		SurveyEventID:       req.SurveyEventID,
		FarmerID:            req.FarmerID,
		FarmerName:          req.FarmerName,
		FarmerAddress:       req.FarmerAddress,
		FarmerMobile:        req.FarmerMobile,
		FarmerEmail:         req.FarmerEmail,
		VillageCode:         villageCode,
		Season:              report.Season(req.Season),
		CropYear:            req.CropYear,
		LossCategory:        report.LossCategory(req.LossCategory),
		CalamityType:        req.CalamityType,
		OccurredAt:          req.OccurredAt,
		FiledAt:             req.FiledAt,
		ExpectedCaptures:    req.ExpectedCaptures,
		ReportedBy:          p.UserID,
		ReportedChannel:     req.ReportedChannel,
		LateReason:          req.LateReason,
		DeclarationAccepted: req.DeclarationAccepted,
		DocumentsPromised:   req.DocumentsPromised,
		FarmerSignatureKind: req.FarmerSignatureKind,
		CropMismatch:        cropMismatch,
		RegistryCrop:        registryCrop,
		Items:               req.Items,
		// The same value the idempotency layer keys on. Carried into the
		// report as well, because the database's unique index is the one
		// guarantee that holds even when the process dies between committing
		// the report and recording that it did.
		ClientRef: clientRef(r),
	})
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	// A retry of a report already filed. Answered with the report and 200,
	// and not audited again: one filing, one audit entry.
	if rep.Replayed {
		w.Header().Set(headerReplayed, "true")
		writeJSON(w, http.StatusOK, rep)
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report.filed", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"reference":        rep.Reference,
			"survey_event":     rep.SurveyEventReference,
			"farmer_id":        rep.FarmerID,
			"calamity":         rep.CalamityType,
			"loss_category":    string(rep.LossCategory),
			"lines":            len(rep.Items),
			"area_affected_ha": rep.TotalAffectedHa(),
			"filed_late":       rep.FiledLate,
			"channel":          rep.ReportedChannel,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusCreated, rep)
}

func (s *Server) listReports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	p, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	// Which farmer's claims a handset is asking for. An account acting for
	// several sends the grant for the one on screen; without it this is the
	// account's own farmer, exactly as before.
	caller := rbac.FromContext(r.Context())
	scope := caller.Scope()
	if caller.IsFarmer() {
		acting, err := s.actingFarmer(r, caller, q.Get("farmer_id"))
		if err != nil {
			writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
			return
		}
		scope.FarmerID = acting
	}

	result, err := s.Reports.List(r.Context(), report.Filter{
		Scope:    scope,
		EventID:  q.Get("survey_event_id"),
		FarmerID: q.Get("farmer_id"),
		State:    q.Get("state"),
		// awaiting=circle|tehsil|district turns the report list into that
		// level's approval queue. Scoped like every other listing, so an
		// officer asking for a rung above their own gets their own villages
		// at that rung and not somebody else's.
		AwaitingLevel: q.Get("awaiting"),
		Page:          p,
	})
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// stuckClaims lists where claims have stopped, for whoever can staff a village.
//
// A claim that cannot be allocated stops quietly: the farmer's app says it is
// with the office, and no officer's queue shows it because it never reached
// one. This is the only place it is visible.
func (s *Server) stuckClaims(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := s.Reports.StuckClaims(ctx, rbac.FromContext(ctx).Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"villages": rows})
}

// retryVillage releases the claims a village had stopped, now that somebody
// has done whatever they were waiting for.
func (s *Server) retryVillage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n, err := s.Reports.RetryVillage(ctx, r.PathValue("villageCode"),
		rbac.FromContext(ctx).Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": n})
}

// claimProgress answers "where has my claim got to, and what is it waiting
// for" -- for the farmer whose claim it is and for the officers who handle it.
//
// One computation, two audiences. What is released differs and what is
// computed does not: see report.ProgressFor, and the note there about why the
// officers' remarks stay sealed until both of them have recorded.
func (s *Server) claimProgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	scope := p.Scope()
	// A farmer acting for one of several people on their handset asks about
	// that farmer's claims. Same rule as the listing: their own farmer needs
	// no grant, anybody else's needs one.
	if p.IsFarmer() {
		acting, err := s.actingFarmer(r, p, r.URL.Query().Get("farmer_id"))
		if err != nil {
			writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
			return
		}
		scope.FarmerID = acting
	}

	prog, err := s.Reports.ProgressFor(ctx, r.PathValue("reportID"), scope, p.IsFarmer())
	if err != nil {
		s.writeReportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, prog)
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request) {
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

	s.Audit.Record(ctx, audit.Event{
		Action: "report.read", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, rep)
}

// printReport renders the report for printing and signing.
//
// Recorded as a distinct audit action from a read: a printed report leaves the
// system on paper, and "who printed this farmer's report" is a question a
// grievance asks that a read log cannot answer.
func (s *Server) printReport(w http.ResponseWriter, r *http.Request) {
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

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report.printed", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail:           map[string]any{"reference": rep.Reference},
	}.FromContext(ctx))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`inline; filename="`+rep.Reference+`.html"`)
	if err := rep.Print(w); err != nil {
		// The header is already written; log rather than try to write a
		// status the client will never see.
		s.Log.Error("rendering report", "id", rep.ID, "error", err)
	}
}

func (s *Server) transitionReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req transitionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	to := report.State(req.State)

	// Acknowledging is the पोहोच पावती — an officer confirming the report
	// reached the administration. A farmer may withdraw their own report and
	// do nothing else.
	if p.IsFarmer() && to != report.StateWithdrawn {
		writeError(w, http.StatusForbidden, "FORBIDDEN",
			"a farmer may withdraw a report, but not move it through review")
		return
	}
	if !p.IsFarmer() && !p.Can(rbac.PermReportReview) {
		writeError(w, http.StatusForbidden, "FORBIDDEN",
			"your role does not carry "+string(rbac.PermReportReview))
		return
	}

	rep, err := s.Reports.Transition(ctx, r.PathValue("reportID"), to,
		p.UserID, p.Scope())
	if err != nil {
		s.writeReportError(w, err)
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "report." + string(to), Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "crop_loss_report", ResourceID: rep.ID,
		JurisdictionCode: rep.Place.VillageCode,
		Detail: map[string]any{
			"reference": rep.Reference,
			"to":        string(rep.State),
			"reason":    req.Reason,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, rep)
}

// reportForm publishes the enumerations the form needs, so a client renders
// the right options rather than hard-coding a list that drifts.
func (s *Server) reportForm(w http.ResponseWriter, _ *http.Request) {
	docs := make([]map[string]string, 0)
	for code, names := range report.KnownDocuments() {
		docs = append(docs, map[string]string{
			"code": code, "mr": names[0], "en": names[1],
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"seasons": []string{"kharif", "rabi", "summer"},
		"loss_categories": []map[string]any{
			{
				"code":       "standing_crop",
				"mr":         "स्थानिक नैसर्गिक आपत्ती",
				"en":         "Localised natural calamity",
				"calamities": report.CalamitiesFor(report.StandingCrop),
			},
			{
				"code":       "post_harvest",
				"mr":         "काढणी पश्चात नुकसान",
				"en":         "Post-harvest loss",
				"calamities": report.CalamitiesFor(report.PostHarvest),
			},
		},
		"documents":           docs,
		"signature_kinds":     []string{"signature", "thumb"},
		"reported_channels":   []string{"field_officer", "farmer_app", "call_centre"},
		"filing_window_hours": int(report.FilingWindow.Hours()),
	})
}

func (s *Server) writeReportError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, report.ErrNotFound):
		writeError(w, http.StatusNotFound, "REPORT_NOT_FOUND",
			"no crop loss report with that id")
	case errors.Is(err, report.ErrNotInArea):
		writeError(w, http.StatusUnprocessableEntity, "NOT_IN_EVENT_AREA", err.Error())
	case errors.Is(err, report.ErrNotYet):
		// A conflict the caller should come back from, not a refusal. The
		// handset keeps the claim and sends it again; see the retryable
		// conflict codes in the app's retry.dart. The message is the real one
		// rather than a generic "try again", because an officer at a counter
		// meets this too and needs to know their district paused the event.
		w.Header().Set("Retry-After", "900")
		writeError(w, http.StatusConflict, "EVENT_PAUSED", err.Error())
	case errors.Is(err, report.ErrAlreadyFiled):
		// Final, and the handset must stop sending: a retry cannot make this
		// succeed. The message names the claim that already exists so the
		// farmer is sent to it rather than left thinking their work vanished.
		writeError(w, http.StatusConflict, "ALREADY_FILED", err.Error())
	case errors.Is(err, report.ErrBadState):
		writeError(w, http.StatusConflict, "INVALID_STATE", err.Error())
	case errors.Is(err, report.ErrBadInput):
		writeError(w, http.StatusBadRequest, "INVALID_REPORT", err.Error())
	case errors.Is(err, report.ErrRetry):
		// Not a verdict on the report. 503 with a wait, which the phone
		// reads as "keep it and send again".
		s.Log.Warn("report write collided; asked the client to retry", "error", err)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "TRY_AGAIN",
			"the report could not be recorded just now; it will be sent again")
	default:
		s.Log.Error("report operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "REPORT_ERROR",
			"the request could not be completed")
	}
}

// checkEventCrop compares the registry's crop for the claimed parcels against
// the crops the survey event covers.
//
// Returns false and an empty name whenever the comparison cannot be made --
// no event, no crops named, nothing in the registry. A check that could not
// run is not a mismatch, and reporting one would put a discrepancy in front of
// an officer that nobody actually observed.
func (s *Server) checkEventCrop(
	ctx context.Context, eventID string, rec *agristack.FarmerRecord,
	items []report.Item,
) (bool, string) {
	if eventID == "" || rec == nil || s.Events == nil {
		return false, ""
	}

	ev, err := s.Events.Get(ctx, eventID)
	if err != nil || ev == nil || len(ev.Crops) == 0 {
		// An event naming no crops covers all of them, and an event we cannot
		// read is not evidence of anything about this farmer.
		return false, ""
	}

	// Any claimed parcel growing a covered crop settles it. A farmer claiming
	// two fields, one soybean and one sugarcane, against a soybean event has
	// not misdeclared anything.
	var firstSeen string
	for _, it := range items {
		if it.FarmID == "" {
			continue
		}
		for _, c := range rec.CropsForParcel(it.FarmID) {
			got := event.MatchCrop(ev.Crops, c.CropCode, c.CropName)
			if got.Matched {
				return false, ""
			}
			if got.Mismatch && firstSeen == "" {
				firstSeen = got.RegistryCrop
			}
		}
	}
	return firstSeen != "", firstSeen
}

// clientRef is the handset's reference for a queued write, when it sent one
// that is well formed. Anything else is ignored rather than refused here: the
// idempotency layer in front has already refused a malformed key.
func clientRef(r *http.Request) string {
	key := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	if !validKey.MatchString(key) {
		return ""
	}
	return key
}
