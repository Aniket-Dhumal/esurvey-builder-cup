package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"
)

// The ticket, checked against the format the relay actually parses.
//
// The signature is recomputed here the way the Python side does it -- hmac-sha256 over
// "expiry.seconds", base64url without padding -- rather than by calling the issuer's own
// helper. Calling the helper would prove only that it agrees with itself, which is
// exactly the mistake already made once in this feature: the officer agent sent a header
// nothing read, and both ends' tests passed because each was checked against its own
// assumption.

const ticketSecret = "a-shared-secret"

// asPythonWouldSignIt mirrors live_ticket.py.
func asPythonWouldSignIt(payload string) string {
	mac := hmac.New(sha256.New, []byte(ticketSecret))
	mac.Write([]byte(payload))
	return strings.TrimRight(
		base64.URLEncoding.EncodeToString(mac.Sum(nil)), "=")
}

func TestTheRelayCanVerifyWhatIsIssued(t *testing.T) {
	now := time.Now()
	ticket := liveTicket(ticketSecret, 480, now)

	parts := strings.Split(ticket, ".")
	if len(parts) != 3 {
		t.Fatalf("a ticket has three parts, got %d: %q", len(parts), ticket)
	}
	payload := parts[0] + "." + parts[1]
	if parts[2] != asPythonWouldSignIt(payload) {
		t.Fatalf("the relay would reject this signature:\n got  %s\n want %s",
			parts[2], asPythonWouldSignIt(payload))
	}
	if parts[1] != "480" {
		t.Fatalf("allowance %q, want 480", parts[1])
	}
}

func TestTheExpiryIsTheWindowAhead(t *testing.T) {
	now := time.Now()
	ticket := liveTicket(ticketSecret, 60, now)
	var expiry int64
	if _, err := fmt.Sscanf(strings.Split(ticket, ".")[0], "%d", &expiry); err != nil {
		t.Fatal(err)
	}
	want := now.Add(LiveTicketWindow).Unix()
	if expiry != want {
		t.Fatalf("expiry %d, want %d", expiry, want)
	}
}

func TestTheWindowIsShortEnoughToBeWorthless(t *testing.T) {
	// A ticket travels in a query string, and query strings reach logs, proxies and
	// crash reports. Two minutes is long enough to negotiate ICE on a slow link and
	// short enough that one read out of a log later is no use.
	if LiveTicketWindow > 5*time.Minute {
		t.Fatalf("a %s window is long enough to be replayed", LiveTicketWindow)
	}
	if LiveTicketWindow < time.Minute {
		t.Fatalf("a %s window will expire during a slow negotiation", LiveTicketWindow)
	}
}

func TestOurOwnVerifierAgreesWithOurIssuer(t *testing.T) {
	now := time.Now()
	seconds, err := verifyLiveTicket(ticketSecret, liveTicket(ticketSecret, 300, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if seconds != 300 {
		t.Fatalf("seconds %d, want 300", seconds)
	}
}

func TestAForgedTicketIsRefused(t *testing.T) {
	now := time.Now()
	forged := liveTicket("not-the-secret", 480, now)
	if _, err := verifyLiveTicket(ticketSecret, forged, now); err == nil {
		t.Fatal("a ticket signed with the wrong secret was accepted")
	}
}

func TestARaisedAllowanceIsRefused(t *testing.T) {
	// The obvious attack: change the number. The signature covers it.
	now := time.Now()
	parts := strings.Split(liveTicket(ticketSecret, 60, now), ".")
	tampered := parts[0] + ".99999." + parts[2]
	if _, err := verifyLiveTicket(ticketSecret, tampered, now); err == nil {
		t.Fatal("a raised allowance was accepted")
	}
}

func TestAnExpiredTicketIsRefused(t *testing.T) {
	issued := time.Now().Add(-2 * LiveTicketWindow)
	if _, err := verifyLiveTicket(
		ticketSecret, liveTicket(ticketSecret, 60, issued), time.Now(),
	); err == nil {
		t.Fatal("an expired ticket was accepted")
	}
}

func TestMalformedTicketsAreRefusedRatherThanPanicking(t *testing.T) {
	now := time.Now()
	for _, bad := range []string{"", "x", "a.b", "a.b.c.d", "....", "no.480.sig"} {
		if _, err := verifyLiveTicket(ticketSecret, bad, now); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestTheTicketCarriesNothingIdentifying(t *testing.T) {
	// It goes in a URL. What it says is "somebody vouched for may talk, for this long",
	// and never who -- because the relay has no use for a farmer id and a query string
	// travels further than the request.
	ticket := liveTicket(ticketSecret, 480, time.Now())
	for _, leak := range []string{"99900000002", "farmer", "aadhaar", "@"} {
		if strings.Contains(ticket, leak) {
			t.Fatalf("the ticket carries %q: %s", leak, ticket)
		}
	}
}
