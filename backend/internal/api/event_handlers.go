package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/event"
	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

type createEventRequest struct {
	CalamityType    string `json:"calamity_type"`
	Title           string `json:"title"`
	Description     string `json:"description,omitempty"`
	NotificationRef string `json:"notification_ref,omitempty"`

	IncidentStart string `json:"incident_start"`
	IncidentEnd   string `json:"incident_end"`

	SurveyOpensAt  *time.Time `json:"survey_opens_at,omitempty"`
	SurveyClosesAt time.Time  `json:"survey_closes_at"`

	// AreaCodes are the talukas and circles covered, by LGD code (talukas) or
	// locally assigned code (circles). No district is asked for: an event is
	// declared for the declaring officer's own district and nowhere else.
	AreaCodes []string `json:"area_codes"`

	// Crops narrows the event to particular crops, by AgriStack code. Omitted
	// or empty covers every crop, which is the right reading of a Collector
	// who did not narrow it: a cyclone does not check what was planted.
	Crops []struct {
		Code string `json:"crop_code"`
		Name string `json:"crop_name"`
	} `json:"crops,omitempty"`
}

// createEvent drafts a calamity notification.
//
// The district is taken from the caller's posting rather than the request
// body. A Collector declares for their own district; letting the body name one
// would make the authorization check an equality test somebody could forget.
func (s *Server) createEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req createEventRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	districtID, err := s.declaringDistrict(p)
	if err != nil {
		writeError(w, http.StatusForbidden, "NO_DISTRICT_POSTING", err.Error())
		return
	}

	switch {
	case req.Title == "" || req.CalamityType == "":
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"calamity_type and title are required")
		return
	case req.IncidentStart == "" || req.IncidentEnd == "":
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"incident_start and incident_end are required")
		return
	case req.SurveyClosesAt.IsZero():
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"survey_closes_at is required; an event without a deadline never ends")
		return
	case !event.ValidCalamity(req.CalamityType):
		writeError(w, http.StatusBadRequest, "INVALID_CALAMITY",
			"calamity_type must be one of: "+joinStrings(event.CalamityTypes()))
		return
	}

	ev, err := s.Events.Create(ctx, event.New{
		CalamityType:    req.CalamityType,
		Title:           req.Title,
		Description:     req.Description,
		NotificationRef: req.NotificationRef,
		Crops:           eventCrops(req),
		DistrictID:      districtID,
		IncidentStart:   req.IncidentStart,
		IncidentEnd:     req.IncidentEnd,
		SurveyOpensAt:   req.SurveyOpensAt,
		SurveyClosesAt:  req.SurveyClosesAt,
		AreaCodes:       req.AreaCodes,
		CreatedBy:       p.UserID,
	})
	if err != nil {
		s.writeEventError(w, err)
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "event.created", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "survey_event", ResourceID: ev.ID,
		JurisdictionCode: ev.DistrictCode, JurisdictionID: ev.DistrictID,
		Detail: map[string]any{
			"reference":      ev.Reference,
			"calamity_type":  ev.CalamityType,
			"areas":          req.AreaCodes,
			"village_count":  ev.VillageCount,
			"incident_start": ev.IncidentStart,
			"incident_end":   ev.IncidentEnd,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusCreated, ev)
}

type transitionRequest struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// transitionEvent declares, opens, closes or cancels an event.
//
// One endpoint rather than four verbs, because the permitted moves are a table
// in the event package and splitting them across routes would put half the
// state machine in the router.
func (s *Server) transitionEvent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)

	var req transitionRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}

	to := event.State(req.State)

	// Declaring and closing are separately permissioned; the rest ride on
	// event:create, which only the Collector holds anyway.
	need := rbac.PermEventCreate
	switch to {
	case event.StateDeclared, event.StateOpen:
		need = rbac.PermEventDeclare
	case event.StateClosed, event.StateComplete, event.StatePaused:
		// Pausing rides with closing rather than with creating: both are
		// acts that stop farmers filing, and the officer trusted to end a
		// filing window is the one trusted to suspend it.
		need = rbac.PermEventClose
	}
	if !p.Can(need) {
		writeError(w, http.StatusForbidden, "FORBIDDEN",
			"your role does not carry "+string(need))
		return
	}

	existing, err := s.Events.Get(ctx, r.PathValue("eventID"))
	if err != nil {
		s.writeEventError(w, err)
		return
	}
	// An event is scoped by its declaring district, not by a village, so the
	// village containment path does not apply here.
	if !s.districtInScope(p, existing.DistrictID) {
		writeError(w, http.StatusForbidden, "OUT_OF_JURISDICTION",
			"this event belongs to another district")
		return
	}

	ev, err := s.Events.Transition(ctx, existing.ID, to, p.UserID)
	if err != nil {
		s.writeEventError(w, err)
		return
	}

	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: "event." + string(to), Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "survey_event", ResourceID: ev.ID,
		JurisdictionCode: ev.DistrictCode, JurisdictionID: ev.DistrictID,
		Detail: map[string]any{
			"reference": ev.Reference,
			"from":      string(existing.State),
			"to":        string(ev.State),
			"reason":    req.Reason,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, ev)
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	pg, err := page.FromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_PAGE", err.Error())
		return
	}

	p := rbac.FromContext(r.Context())

	// An officer with no posting sees nothing. An empty scope reaches the
	// store as "no restriction" -- which is what a farmer, who holds no
	// jurisdiction, is meant to get -- and an officer whose postings had all
	// been ended was being shown every event in every state.
	if !p.IsFarmer() && len(p.Scope().JurisdictionIDs) == 0 {
		writeJSON(w, http.StatusOK, page.Wrap([]*event.Event{}, 0, pg))
		return
	}

	result, err := s.Events.List(r.Context(), event.ListFilter{
		JurisdictionIDs: p.Scope().JurisdictionIDs,
		State:           q.Get("state"),
		// ?village=…&village=… : the handset sends its farmer's parcel
		// villages, so it is offered only events it can actually file against.
		// Capped, because each one is a coverage check per event.
		Villages: capStrings(q["village"], 50),
		Page:     pg,
	})
	if err != nil {
		s.writeEventError(w, err)
		return
	}

	// What a claim against each of these may be filed under. The handset
	// renders the list rather than deciding it; see loss_categories.go.
	withLossCategories(result.Items)

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getEvent(w http.ResponseWriter, r *http.Request) {
	p := rbac.FromContext(r.Context())

	// A farmer may read any declared event: a declaration is a public notice,
	// and the list they are offered is already narrowed to their villages. An
	// officer reads only events in or over their own area -- by id this used
	// to answer any event in any state.
	var ev *event.Event
	var err error
	if p.IsFarmer() {
		ev, err = s.Events.Get(r.Context(), r.PathValue("eventID"))
	} else {
		ev, err = s.Events.GetVisible(r.Context(), r.PathValue("eventID"),
			p.Scope().JurisdictionIDs)
	}
	if err != nil {
		s.writeEventError(w, err)
		return
	}

	// What a claim against this event may be filed under; see
	// loss_categories.go.
	withLossCategories([]*event.Event{ev})

	writeJSON(w, http.StatusOK, ev)
}

// checkEventEligibility answers whether a parcel may be surveyed against an
// event. The mobile app calls it before letting a surveyor start, so the
// refusal arrives before the walk rather than after the upload.
func (s *Server) checkEventEligibility(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	farmerID := q.Get("farmer_id")
	farmID := q.Get("farm_id")
	incidentDate := q.Get("incident_date")
	if farmerID == "" || farmID == "" || incidentDate == "" {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"farmer_id, farm_id and incident_date are required")
		return
	}

	// Whose parcel may be looked up. It answers from the registry by farmer
	// id, so unguarded it told any caller whether a farmer in any state held a
	// given parcel. A farmer checks their own; an officer, a parcel in their
	// area -- and anything else is answered exactly as a parcel that does not
	// exist.
	p := rbac.FromContext(ctx)
	if p.IsFarmer() {
		acting, err := s.actingFarmer(r, p, farmerID)
		if err != nil || acting != farmerID {
			writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND",
				"that farmer has no such parcel")
			return
		}
	}

	rec, err := s.Registry.FarmerRecord(ctx, farmerID)
	if err != nil {
		s.writeFarmerRecordError(w, err)
		return
	}
	parcel, ok := rec.ParcelByFarmID(farmID)
	if !ok {
		writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND",
			"that farmer has no such parcel")
		return
	}
	if !p.IsFarmer() {
		in, err := s.Identity.VillagesInScope(ctx,
			[]string{parcel.VillageLGDCode}, p.Scope().JurisdictionIDs)
		if err != nil {
			s.Log.Error("scoping an eligibility check", "error", err)
			writeError(w, http.StatusInternalServerError, "SCOPE_FAILED", "")
			return
		}
		if !in[parcel.VillageLGDCode] {
			writeError(w, http.StatusNotFound, "PARCEL_NOT_FOUND",
				"that farmer has no such parcel")
			return
		}
	}

	elig, err := s.Events.CheckParcel(ctx, r.PathValue("eventID"),
		parcel.VillageLGDCode, incidentDate, time.Now().UTC())
	if err != nil {
		s.writeEventError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"event_id":         r.PathValue("eventID"),
		"farm_id":          farmID,
		"village_lgd_code": parcel.VillageLGDCode,
		"eligible":         elig.Eligible,
		"reason":           elig.Reason,
	})
}

// calamityTypes publishes the closed set, so a form does not hard-code it.
func (s *Server) calamityTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"calamity_types": event.CalamityTypes(),
	})
}

// declaringDistrict returns the district the caller may declare for.
func (s *Server) declaringDistrict(p *rbac.Principal) (string, error) {
	for _, g := range p.Grants {
		if g.Role == rbac.RoleDistrictMagistrate && g.JurisdictionLevel == rbac.LevelDistrict {
			return g.JurisdictionID, nil
		}
	}
	return "", errors.New(
		"only an officer posted as District Magistrate at district level may " +
			"declare a survey event")
}

// districtInScope reports whether a district lies at or under the caller's
// postings.
func (s *Server) districtInScope(p *rbac.Principal, districtID string) bool {
	for _, g := range p.Grants {
		if g.JurisdictionID == districtID {
			return true
		}
	}
	return false
}

func (s *Server) writeEventError(w http.ResponseWriter, err error) {
	// A malformed id is the caller's mistake; see ids.go. Checked first so it
	// cannot fall through to the 500 below.
	if badRequestFromDB(w, err) {
		return
	}
	switch {
	case errors.Is(err, event.ErrNotFound):
		writeError(w, http.StatusNotFound, "EVENT_NOT_FOUND", err.Error())
	case errors.Is(err, event.ErrAreaInUse):
		// 409, and the message is kept: it names the circle and counts the
		// reports holding it, which is the whole of what a Collector needs to
		// decide what to do instead. Falling through to the default would
		// have replaced that with "the request could not be completed".
		writeError(w, http.StatusConflict, "AREA_IN_USE", err.Error())
	case errors.Is(err, event.ErrBadArea):
		writeError(w, http.StatusBadRequest, "INVALID_AREA", err.Error())
	case errors.Is(err, event.ErrBadState):
		writeError(w, http.StatusConflict, "INVALID_STATE", err.Error())
	case errors.Is(err, event.ErrNotEligible), errors.Is(err, event.ErrOutOfWindow),
		errors.Is(err, event.ErrSurveyClosed):
		writeError(w, http.StatusUnprocessableEntity, "NOT_ELIGIBLE", err.Error())
	default:
		s.Log.Error("event operation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "EVENT_ERROR",
			"the request could not be completed")
	}
}

func joinStrings(in []string) string {
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}

// eventCrops converts the request's crops into the domain type.
func eventCrops(req createEventRequest) []event.Crop {
	out := make([]event.Crop, 0, len(req.Crops))
	for _, c := range req.Crops {
		out = append(out, event.Crop{Code: c.Code, Name: c.Name})
	}
	return out
}

// --- editing coverage ------------------------------------------------------

type areasRequest struct {
	// AreaCodes are LGD codes of talukas or circles.
	AreaCodes []string `json:"area_codes"`
	Reason    string   `json:"reason,omitempty"`
}

// editEventAreas adds or removes talukas and circles on a running event.
//
// One handler for both directions, because the authorization, the scope check
// and the audit record are identical and the only difference is which store
// method runs. Two handlers would be two places to forget the district check.
func (s *Server) editEventAreas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	adding := r.PathValue("op") == "add"

	var req areasRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return
	}
	if len(req.AreaCodes) == 0 {
		writeError(w, http.StatusBadRequest, "MISSING_FIELD",
			"name at least one taluka or circle")
		return
	}

	existing, err := s.Events.Get(ctx, r.PathValue("eventID"))
	if err != nil {
		s.writeEventError(w, err)
		return
	}
	if !s.districtInScope(p, existing.DistrictID) {
		writeError(w, http.StatusForbidden, "OUT_OF_JURISDICTION",
			"this event belongs to another district")
		return
	}

	var ev *event.Event
	if adding {
		ev, err = s.Events.AddAreas(ctx, existing.ID, req.AreaCodes, p.UserID)
	} else {
		ev, err = s.Events.RemoveAreas(ctx, existing.ID, req.AreaCodes, p.UserID)
	}
	if err != nil {
		s.writeEventError(w, err)
		return
	}

	action := "event.areas_removed"
	if adding {
		action = "event.areas_added"
	}
	_ = s.Audit.RecordSync(ctx, audit.Event{
		Action: action, Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "survey_event", ResourceID: ev.ID,
		JurisdictionCode: ev.DistrictCode, JurisdictionID: ev.DistrictID,
		Detail: map[string]any{
			"reference":  ev.Reference,
			"area_codes": req.AreaCodes,
			"reason":     req.Reason,
			// The count after the edit, so the record says what the event
			// covered rather than only what changed.
			"villages_now": ev.VillageCount,
		},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, ev)
}

// capStrings keeps at most n values.
func capStrings(v []string, n int) []string {
	if len(v) > n {
		return v[:n]
	}
	return v
}
