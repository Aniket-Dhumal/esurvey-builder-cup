package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/identity"
	"github.com/agribid/esurvey-platform/backend/internal/livespend"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// Opening a live guidance session, and recording what it cost.
//
// # Why the handset asks here first
//
// Because three things have to be true before a session is worth opening, and only
// one of them is something the phone can answer.
//
// The phone knows whether the link can carry it -- see the network probe. This knows
// whether there is budget left today, and it holds the credential the relay needs.
// Asking here is also where a refusal can be explained: "the assistant has finished
// for today" is a sentence a farmer can act on, and it is not one the handset could
// have worked out.
//
// # Why a refusal costs the farmer nothing
//
// Every instruction needed to file a claim is scripted, works with no network, and is
// unchanged by any of this. So the assistant can be refused -- over budget, over a
// slow link, not configured at all -- without refusing the farmer anything. That is
// what makes a hard budget safe to enforce.

// liveSessionGrant is what the handset is given.
type liveSessionGrant struct {
	// URL the handset opens. Carries the claim's context as query parameters and
	// nothing identifying: see the relay, which accepts a fixed list.
	URL string `json:"url"`

	// MaxSeconds is the shorter of the relay's own ceiling and what is left of
	// today's allowance, so a farmer with four minutes left gets four rather than
	// being cut off mid-sentence at eight.
	MaxSeconds int `json:"max_seconds"`

	// Model is named so a claim's guidance can be read later knowing what produced
	// it.
	Model string `json:"model"`
}

// startLiveSession grants a session, or says why not.
func (s *Server) startLiveSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in first")
		return
	}

	if s.LiveRelayURL == "" {
		// Not configured. Said as unavailability rather than refusal: nothing about
		// the request was wrong.
		writeError(w, http.StatusServiceUnavailable, "LIVE_UNAVAILABLE",
			"live guidance is not configured")
		return
	}

	var req struct {
		FarmerID string `json:"farmer_id"`
		ReportID string `json:"report_id,omitempty"`

		// What the claim is about, for the assistant's own context. Deliberately a
		// short fixed set: the relay accepts only these, and adding to it is a
		// decision about what leaves the platform.
		Crop     string  `json:"crop,omitempty"`
		Calamity string  `json:"calamity,omitempty"`
		Season   string  `json:"season,omitempty"`
		Stage    string  `json:"stage,omitempty"`
		ExtentHa float64 `json:"extent_ha,omitempty"`

		// Where the claim is, as the handset is able to say it. Neither of these
		// is what travels to the model: see liveDistrict, which answers that
		// from the farmer's own record. They are kept because a handset that
		// knows the code is worth listening to when the registry says nothing,
		// and because a field that is no longer sent is better refused here than
		// silently trusted by whatever is written next.
		DistrictLGDCode string `json:"district_lgd_code,omitempty"`
		District        string `json:"district,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).
		Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"the request could not be read")
		return
	}

	// Whose claim this is. A handset acts for several farmers, so the farmer is the
	// one named rather than the one signed in -- and the cap is counted against
	// them.
	farmerID := strings.TrimSpace(req.FarmerID)
	if farmerID == "" {
		farmerID = p.FarmerID
	}
	if farmerID == "" {
		writeError(w, http.StatusBadRequest, "NO_FARMER",
			"say which farmer this session is for")
		return
	}

	// A handset may only open a session for a farmer it is entitled to act for. The
	// same check the capture routes make, for the same reason: without it a farmer
	// id is all anybody needs to spend somebody else's allowance.
	if _, err := s.actingScope(r, p); err != nil {
		writeError(w, http.StatusForbidden, "NOT_YOUR_FARMER", err.Error())
		return
	}

	seconds := 0
	if s.LiveSpend != nil {
		left, err := s.LiveSpend.MayStart(ctx, farmerID, p.UserID)
		if err != nil {
			if errors.Is(err, livespend.ErrOverBudget) {
				// Named so the handset says the right thing: the assistant is
				// finished for today, the walk is not, and nothing about the claim
				// has changed.
				writeError(w, http.StatusTooManyRequests, "LIVE_OVER_BUDGET",
					"the assistant has reached today's limit; the walk is "+
						"unaffected")
				return
			}
			writeError(w, http.StatusInternalServerError, "LIVE_FAILED",
				"the assistant could not be started")
			return
		}
		seconds = left
	}

	// A signed permission for the relay, which cannot check the farmer itself.
	//
	// Without it the relay is an open door onto a metered model: an endpoint a handset
	// can reach is an endpoint anybody can reach, and a WebSocket anybody can hold
	// open produces a bill a month later. The ticket says only that the platform
	// vouched for somebody and for how long -- never who they are, because it travels
	// in a query string and that goes through logs and proxies.
	grant := liveSessionGrant{
		URL: s.liveURL(
			req.Crop, req.Calamity, req.Season,
			s.liveDistrict(ctx, farmerID, req.DistrictLGDCode, req.District),
			liveTicket(s.LiveTicketSecret, seconds, time.Now()),
		),
		MaxSeconds: seconds,
		Model:      s.LiveModel,
	}

	s.Audit.Record(ctx, audit.Event{
		Action: "live.session.grant", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "live_session", ResourceID: req.ReportID,
		Detail: map[string]any{"max_seconds": seconds},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, grant)
}

// liveURL builds the relay address with only the agreed context on it.
//
// An explicit list rather than passing the request through: a query string assembled
// from whatever arrived is a query string that carries whatever a future caller adds,
// and this one crosses the boundary to the model.
func (s *Server) liveURL(crop, calamity, season, district, ticket string) string {
	q := url.Values{}
	for key, value := range map[string]string{
		"crop":     crop,
		"calamity": calamity,
		"season":   season,
		"district": district,
	} {
		if v := strings.TrimSpace(value); v != "" {
			q.Set(key, v)
		}
	}
	// Always present, and the relay refuses without it.
	q.Set("ticket", ticket)
	return s.LiveRelayURL + "?" + q.Encode()
}

// districtNames answers the one question this path has about a district.
//
// Narrowed to that question rather than taking the identity store whole: the
// live path has no business reading accounts or grants, and an interface this
// small can be stood up wherever the reference table is not. *identity.Store
// satisfies it, and Identity is what serves it unless something else is set.
type districtNames interface {
	JurisdictionByCode(
		ctx context.Context, level rbac.Level, code string,
	) (*identity.Jurisdiction, error)
}

// liveDistrictBudget is how long resolving a district may take before the
// session opens without one.
//
// Short because of where this sits: the farmer has pressed the button and is
// waiting, and the district is the least of what the assistant needs. One
// lookup on a unique index is a millisecond or two, so anything near this
// bound is a reference table in trouble rather than a slow row.
const liveDistrictBudget = 750 * time.Millisecond

// liveDistrict decides which district, if any, travels with the session.
//
// # Why the platform answers this and not the handset
//
// Because the handset cannot. The farmer app holds an LGD code and nothing
// that turns it into a place, so anything it sends is either that code or a
// guess at the name. The code is no use to the model, which cannot read 521 as
// Pune, and the guess must not become one, because the rule this data is
// governed by is that a code is never invented and a name derived from one
// would be exactly that.
//
// The platform does know: the farmer whose session this is has a district on
// their registry record, and that farmer has already been authorised further
// up this handler. So the party that knows is the party that answers, and what
// the handset claimed is only consulted when the record says nothing.
//
// A free-text district is taken at face value once it cannot be a code. All
// digits is refused rather than passed through, which is the only way a caller
// could otherwise smuggle a code in as a name.
//
// # Why nothing is also an answer
//
// The relay's prompt reads perfectly with no district at all, so every failure
// here -- an unknown code, a lookup that errored, a table too slow to answer
// within the budget above -- ends in the same harmless place. That is the point:
// a farmer standing in a field must not lose the assistant because a reference
// table was having a bad afternoon.
func (s *Server) liveDistrict(
	ctx context.Context, farmerID, sentCode, sentName string,
) string {
	ctx, cancel := context.WithTimeout(ctx, liveDistrictBudget)
	defer cancel()

	// The farmer's own record first, then what the handset offered -- which is
	// worth trying only because a registry that has no district for a farmer
	// has told us nothing, rather than told us there is none.
	for _, code := range []string{
		s.farmerDistrictCode(ctx, farmerID), sentCode,
	} {
		if name := s.districtName(ctx, code); name != "" {
			return name
		}
	}

	name := strings.TrimSpace(sentName)
	if name == "" || looksLikeACode(name) {
		return ""
	}
	return name
}

// farmerDistrictCode reads the district off the farmer's registry record.
func (s *Server) farmerDistrictCode(ctx context.Context, farmerID string) string {
	if s.Registry == nil || farmerID == "" {
		return ""
	}
	rec, err := s.Registry.FarmerRecord(ctx, farmerID)
	if err != nil || rec == nil {
		// Debug, not warn. AgriStack being unreachable is reported loudly by
		// every route whose answer depends on it, and this one's does not: the
		// session opens either way, so a warning here would only add noise to
		// an outage that is already being shouted about.
		s.Log.Debug("no registry district for live guidance",
			"farmer_id", farmerID, "error", err)
		return ""
	}
	return rec.Farmer.DistrictLGDCode
}

// districtName resolves an LGD district code to the district's own name.
func (s *Server) districtName(ctx context.Context, code string) string {
	code = strings.TrimSpace(code)

	// Identity is what answers this in a deployment, since it is the store the
	// jurisdiction tree is read from anyway. Districts is for a caller holding
	// the same reference data elsewhere, and is how this path is exercised
	// without a jurisdiction table standing behind it.
	names := s.Districts
	if names == nil && s.Identity != nil {
		names = s.Identity
	}
	if code == "" || names == nil {
		return ""
	}

	jur, err := names.JurisdictionByCode(ctx, rbac.LevelDistrict, code)
	if err != nil {
		if !errors.Is(err, identity.ErrNotFound) {
			s.Log.Warn("resolving a district for live guidance",
				"code", code, "error", err)
		}
		return ""
	}
	return jur.Name
}

// looksLikeACode reports whether a supposed district name is really a number.
//
// LGD codes are digits throughout, and no district in the country is named one.
// So this is not a heuristic that might misjudge a real place: it is the
// difference between a name and a code, and the model is only ever told names.
func looksLikeACode(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// endLiveSession records what a finished session cost.
//
// Reported by the handset rather than by the relay. The relay knows the figures, but
// the handset knows which farmer and which claim it was for -- and a relay told those
// would be a relay holding identifying data it has no other use for.
//
// A handset that never reports leaves a session uncounted, which undercounts the cap.
// That is the right way round: a farmer then gets slightly more than their allowance,
// whereas overcounting refuses time nobody used.
func (s *Server) endLiveSession(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in first")
		return
	}
	if s.LiveSpend == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	var req struct {
		FarmerID      string `json:"farmer_id"`
		ReportID      string `json:"report_id,omitempty"`
		Seconds       int    `json:"seconds"`
		AudioBytesIn  int64  `json:"audio_bytes_in,omitempty"`
		AudioBytesOut int64  `json:"audio_bytes_out,omitempty"`
		FramesIn      int    `json:"frames_in,omitempty"`
		EndedBecause  string `json:"ended_because,omitempty"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).
		Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"the report could not be read")
		return
	}

	farmerID := strings.TrimSpace(req.FarmerID)
	if farmerID == "" {
		farmerID = p.FarmerID
	}

	// Bounded against a handset reporting nonsense. The relay's own ceiling is eight
	// minutes, so anything far past it is a wrong figure rather than a long session,
	// and letting it through would exhaust a farmer's day in one row.
	const plausibleCeiling = 30 * 60
	if req.Seconds > plausibleCeiling {
		req.Seconds = plausibleCeiling
	}

	now := time.Now()
	if err := s.LiveSpend.Record(ctx, livespend.Event{
		FarmerID:      farmerID,
		ReportID:      req.ReportID,
		AccountUserID: p.UserID,
		OpenedAt:      now.Add(-time.Duration(req.Seconds) * time.Second),
		ClosedAt:      now,
		Seconds:       req.Seconds,
		AudioBytesIn:  req.AudioBytesIn,
		AudioBytesOut: req.AudioBytesOut,
		FramesIn:      req.FramesIn,
		EndedBecause:  req.EndedBecause,
		Model:         s.LiveModel,
	}); err != nil {
		// Logged and accepted. A handset that cannot report its usage must not be
		// made to retry: the farmer has finished walking and the retry would be the
		// only thing left running.
		s.Log.Warn("recording a live session failed", "error", err)
	}

	w.WriteHeader(http.StatusNoContent)
}
