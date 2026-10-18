package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// A ticket a handset carries to the relay.
//
// # Why the relay cannot just check the farmer's token
//
// Because it does not have the means to. The relay is the AI service: it holds the
// model credential and knows nothing about sessions, jurisdictions or farmers, and
// teaching it to verify platform tokens would put a second copy of the authentication
// rules in a process whose job is audio.
//
// # Why something is needed at all
//
// Because without it the relay is an open door onto a metered model. A WebSocket that
// accepts anybody is a WebSocket that anybody can hold open, and the bill arrives a
// month later. The endpoint has to be reachable from a handset -- that is the whole
// point -- so "it is on an internal network" stops being true the moment the feature
// works.
//
// So the platform, which has already authenticated the farmer and checked their
// budget, issues a short-lived ticket. The relay verifies a signature and an expiry
// and nothing else: it learns that somebody the platform vouched for may talk, for
// this long, and never who they are.
//
// # What is deliberately not in it
//
// The farmer id. The relay has no use for it -- the context it needs is the crop and
// the calamity -- and a ticket in a URL travels through logs and proxies. What it
// carries is a permission and a deadline.

// LiveTicketWindow is how long a ticket may be used to open a session.
//
// Two minutes. Long enough to survive a handset negotiating ICE on a slow link, short
// enough that one captured from a log is worthless by the time anybody reads it. It
// bounds when a session may *start*; how long it may run is the session's own limit.
const LiveTicketWindow = 2 * time.Minute

// liveTicket signs a permission to open one session.
//
// Format is `expiry.seconds.signature`, all base64url. Deliberately not JSON: it goes
// in a query string, it is read by two processes and nothing else, and a format with
// no parser has no parser bugs.
func liveTicket(secret string, maxSeconds int, now time.Time) string {
	expiry := now.Add(LiveTicketWindow).Unix()
	payload := fmt.Sprintf("%d.%d", expiry, maxSeconds)
	return payload + "." + signTicket(secret, payload)
}

func signTicket(secret, payload string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifyLiveTicket is the same check the relay makes, kept here so it can be tested
// against the issuer rather than described in two places.
//
// Returns the seconds the ticket allows, or an error. The signature is compared in
// constant time: a timing oracle on the prefix is a slow way to forge one, and slow is
// not the same as impossible.
func verifyLiveTicket(secret, ticket string, now time.Time) (int, error) {
	parts := strings.Split(ticket, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("a ticket has three parts")
	}
	payload := parts[0] + "." + parts[1]
	if !hmac.Equal([]byte(parts[2]), []byte(signTicket(secret, payload))) {
		return 0, fmt.Errorf("that ticket was not issued here")
	}
	expiry, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unreadable expiry")
	}
	if now.Unix() > expiry {
		return 0, fmt.Errorf("that ticket has expired")
	}
	seconds, err := strconv.Atoi(parts[1])
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("unreadable allowance")
	}
	return seconds, nil
}
