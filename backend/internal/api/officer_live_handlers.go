package api

import (
	"net/http"
	"net/url"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/audit"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// An officer asking their question out loud instead of typing it.
//
// # Why speech is a transport and not a second assistant
//
// Because the assistant already exists, in officeragent, and its whole worth is what
// it cannot do: it reads and drafts, it has no tool that approves, returns, reassigns
// or closes a claim, and no tool that changes a figure. Those limits are enforced in
// Go, beside the tools, and tested there.
//
// A model that heard the officer and answered them would be a second assistant with
// none of that -- reached by speaking rather than typing, which is not a security
// boundary anybody would design on purpose. So the spoken half does one thing: it
// carries the words. The relay writes down what was said, the console posts that text
// to the same endpoint it posts a typed question to, and every guardrail stays exactly
// where it is.
//
// It follows that this route grants no tool, sees no claim, and passes nothing about
// one to the relay. The only thing it hands out is permission to speak.
//
// # Why this is a route of its own rather than a flag on the farmer's
//
// Three reasons, and each alone would be enough.
//
// The permission differs. A farmer's session is behind survey:capture, which the
// adjudicating roles deliberately do not hold -- a Tehsildar who could create evidence
// and then weigh it is the separation this platform was built to keep. An officer using
// the assistant holds report:read, which is what the assistant's own route asks for.
// A guard is stated in the route table and cannot be branched on inside a handler.
//
// The accounting differs. A farmer's session is counted against that farmer's daily
// allowance and their handset's, in livespend. An officer's is not a farmer's session,
// there is no farmer to charge it to, and inventing one would put a stranger's
// identifier on an officer's own dictation in order to bill them for it.
//
// The request differs. A farmer's grant names a farmer, is refused without one, and
// carries the claim's crop and calamity to the relay. An officer's names nobody and
// carries nothing. A flag that switched all of that off would leave a handler whose two
// halves shared only a ticket.

// officerLiveMode is the relay's name for a session that only listens.
//
// The same word the relay reads out of the query string; see live.TRANSCRIBE_MODE
// there. Spelled in two languages because the two processes are written in two, which
// is also true of the ticket's format, and both are pinned by a test on each side.
const officerLiveMode = "transcribe"

// officerLiveSeconds is how long one spoken question may take.
//
// Two minutes. A question is a sentence -- "how many claims are waiting on me", "why is
// CLR-2026-496-00001 short of evidence" -- and two minutes is a long and careful one.
// The console opens a session per question and closes it when the officer stops
// talking, so this bounds the case where nobody closed anything: a console left open on
// a desk with a live microphone costs two minutes and not an afternoon.
//
// Deliberately not the relay's own eight. Eight is a farmer walking a field with pauses
// between points, which is a different shape of conversation.
const officerLiveSeconds = 2 * 60

// officerVoiceGrant is what the console is given.
//
// No URL, unlike the farmer's grant. The console already knows the one origin it talks
// to -- it is the origin it fetched every other answer from -- and a second copy of a
// base address in a browser build is how a production console ends up pointed at a
// developer's machine. So the platform says what is permitted and the browser says
// where, from the value it already has.
type officerVoiceGrant struct {
	// Ticket is the signed permission the relay checks. It names nobody: see
	// live_ticket.go, which explains at length why not.
	Ticket string `json:"ticket"`

	// MaxSeconds is how long the session may run once opened.
	MaxSeconds int `json:"max_seconds"`

	// Mode is the relay's name for the listening-only session, passed through rather
	// than spelled again in the browser.
	Mode string `json:"mode"`
}

// officerVoice decides what an officer's spoken question is allowed.
//
// Separated from the handler because there is a decision here worth reading on its own:
// a fixed short allowance, a ticket that identifies nobody, and no claim. Everything
// this does not consult -- the farmer's allowance, the report on screen, the registry
// -- is the substance of the route.
func (s *Server) officerVoice(now time.Time) officerVoiceGrant {
	return officerVoiceGrant{
		Ticket:     liveTicket(s.LiveTicketSecret, officerLiveSeconds, now),
		MaxSeconds: officerLiveSeconds,
		Mode:       officerLiveMode,
	}
}

// startOfficerVoice grants an officer permission to dictate one question.
func (s *Server) startOfficerVoice(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := rbac.FromContext(ctx)
	if p == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "sign in first")
		return
	}

	// A farmer holds report:read, for their own claims, so the route guard alone does
	// not keep a handset out of here -- and a handset that got in would have an
	// unmetered session, since nothing on this path consults livespend. The farmer's
	// own route is the one with the allowance on it, and it is the one they must use.
	if p.FarmerID != "" {
		writeError(w, http.StatusForbidden, "OFFICER_ONLY",
			"a handset opens a session through the farmer's own route")
		return
	}

	if s.AIServiceURL == "" {
		// Not configured. Said as unavailability rather than refusal: nothing about
		// the request was wrong, and the console's typed chat is unaffected.
		writeError(w, http.StatusServiceUnavailable, "LIVE_UNAVAILABLE",
			"dictation is not configured")
		return
	}

	grant := s.officerVoice(time.Now())

	// Recorded without a resource, because there is none. What happened is that an
	// officer was allowed to speak for two minutes; what they then asked is a question
	// to the assistant, and that route records its own.
	s.Audit.Record(ctx, audit.Event{
		Action: "live.officer.grant", Outcome: audit.OutcomeAllow,
		ActorIP: clientIP(r, s.TrustedProxyHops), RequestID: RequestID(ctx),
		ResourceType: "live_session",
		Detail:       map[string]any{"max_seconds": grant.MaxSeconds},
	}.FromContext(ctx))

	writeJSON(w, http.StatusOK, grant)
}

// officerRelayQuery is what reaches the relay when an officer speaks.
//
// Built rather than forwarded, and that is the point. The console could put a crop, a
// claim reference or a farmer id on the socket's query string, and the relay would pass
// whatever it recognised to the model; a caller could also leave the mode off and get a
// talking farmer session on an officer's ticket. Neither is a thing the browser should
// be trusted not to do, so neither is possible: two parameters leave here, and the mode
// is set by the platform.
func officerRelayQuery(in url.Values) url.Values {
	out := url.Values{}
	// Always present, and the relay refuses without it.
	out.Set("ticket", in.Get("ticket"))
	out.Set("mode", officerLiveMode)
	return out
}
