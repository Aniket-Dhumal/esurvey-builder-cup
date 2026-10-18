package api

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/livespend"
)

// What an officer's spoken question is allowed to be.
//
// The properties worth pinning are all absences: no farmer, no claim, no spend against
// anybody, and no way for the browser to ask for a session that talks back. Each of
// those is a thing that would work perfectly if it were wrong.

func officerVoiceServer() *Server {
	return &Server{
		AIServiceURL:     "http://relay.internal:8099",
		LiveTicketSecret: ticketSecret,
	}
}

func TestAnOfficersVoiceTicketVerifiesAgainstTheCheckTheRelayMakes(t *testing.T) {
	// Verified with the same function the relay's own check is mirrored from, rather
	// than by inspecting the string: a ticket the relay would refuse is a talk button
	// that closes the socket the moment it opens, and nothing else here would notice.
	now := time.Now()
	grant := officerVoiceServer().officerVoice(now)

	seconds, err := verifyLiveTicket(ticketSecret, grant.Ticket, now)
	if err != nil {
		t.Fatalf("the relay would refuse this ticket: %v", err)
	}
	if seconds != officerLiveSeconds {
		t.Errorf("the ticket allows %ds, the grant says %ds; the relay believes the "+
			"ticket and the console believes the grant", seconds, grant.MaxSeconds)
	}
	if grant.MaxSeconds != officerLiveSeconds {
		t.Errorf("max_seconds is %d, want %d", grant.MaxSeconds, officerLiveSeconds)
	}
}

func TestAnOfficersVoiceTicketExpiresLikeAFarmersDoes(t *testing.T) {
	// The window bounds when a session may start, not how long it may run. A ticket
	// still usable an hour after it was handed out is free audio for whoever read the
	// log it went through.
	now := time.Now()
	grant := officerVoiceServer().officerVoice(now)

	if _, err := verifyLiveTicket(
		ticketSecret, grant.Ticket, now.Add(LiveTicketWindow+time.Second),
	); err == nil {
		t.Fatal("a ticket past its window was accepted")
	}
}

func TestAnOfficersVoiceSessionIsNotCountedAgainstAnyFarmersAllowance(t *testing.T) {
	// An officer dictating is not a farmer walking a field, so there is no farmer to
	// charge it to and nothing is charged. The alternative -- naming some farmer to
	// satisfy the ledger -- would bill a stranger for an officer's own words.
	//
	// Proved by giving the server a spend store with no database behind it. Anything
	// that asked it a question would fail on the spot, so a grant that comes back is a
	// grant that never asked.
	s := officerVoiceServer()
	s.LiveSpend = livespend.New(nil)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("granting an officer's dictation consulted the farmer spend "+
				"ledger: %v", r)
		}
	}()

	if got := s.officerVoice(time.Now()); got.Ticket == "" {
		t.Fatal("no ticket was issued")
	}
}

func TestAnOfficersVoiceGrantNamesNobodyAndNoClaim(t *testing.T) {
	// Asserted on the wire form rather than the struct, because the wire form is what
	// crosses to the browser and then, via the query string, to the relay. A field
	// added here for convenience is a field that ends up in somebody's proxy log.
	raw, err := json.Marshal(officerVoiceServer().officerVoice(time.Now()))
	if err != nil {
		t.Fatalf("the grant does not marshal: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the grant does not round-trip: %v", err)
	}

	allowed := map[string]bool{"ticket": true, "max_seconds": true, "mode": true}
	for key := range got {
		if !allowed[key] {
			t.Errorf("the grant carries %q; an officer's dictation is permission to "+
				"speak and nothing about anybody", key)
		}
	}
	if got["mode"] != officerLiveMode {
		t.Errorf("mode is %v, want %q", got["mode"], officerLiveMode)
	}
}

func TestAnOfficersSocketReachesTheRelayInTranscribeOnlyMode(t *testing.T) {
	// The mode is stated by the platform, not asked for by the browser. A console that
	// left it off -- or a console somebody else wrote -- would otherwise get a session
	// that speaks back and answers, which is the whole thing this design refuses.
	got := officerRelayQuery(url.Values{"ticket": {"t-1"}})

	if got.Get("mode") != officerLiveMode {
		t.Errorf("mode is %q, want %q", got.Get("mode"), officerLiveMode)
	}
	if got.Get("ticket") != "t-1" {
		t.Errorf("ticket is %q; the relay refuses a socket without one", got.Get("ticket"))
	}
}

func TestAnOfficersSocketCarriesNothingTheBrowserPutOnIt(t *testing.T) {
	// The console has a claim on screen and could put any of it here. The transcribing
	// session has no use for a claim and must not be told one, so the query is built
	// rather than forwarded and this is what that buys.
	got := officerRelayQuery(url.Values{
		"ticket":    {"t-1"},
		"mode":      {"guidance"},
		"crop":      {"Soybean"},
		"district":  {"Solapur"},
		"farmer_id": {"99900000002"},
	})

	if got.Get("mode") != officerLiveMode {
		t.Errorf("the browser chose the mode: got %q", got.Get("mode"))
	}
	for _, key := range []string{"crop", "district", "farmer_id"} {
		if got.Has(key) {
			t.Errorf("%q reached the relay on an officer's socket", key)
		}
	}
	if len(got) != 2 {
		t.Errorf("the socket carries %d parameters: %v; want the ticket and the mode",
			len(got), got)
	}
}
