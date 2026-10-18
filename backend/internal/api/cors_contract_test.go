package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// What the browser is allowed to send.
//
// These are the failures no handler test can see. A handler test calls the
// handler; a browser asks permission first, and a method or header missing
// from that answer means the request is never made at all. The symptom is a
// console button that does nothing, on a deployment where the console and the
// API are on different origins -- which is the deployment the CORS middleware
// exists for.

func preflight(t *testing.T, origin, method string) http.Header {
	t.Helper()
	h := cors([]string{"https://console.example"}, http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))

	r := httptest.NewRequest(http.MethodOptions, "/api/v1/reports/x/verify", nil)
	r.Header.Set("Origin", origin)
	r.Header.Set("Access-Control-Request-Method", method)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Header()
}

func TestEveryMethodTheConsoleUsesIsAllowed(t *testing.T) {
	// PUT was missing. The one route that needs it is an officer amending
	// their own verification after reading their colleague's, so revising a
	// finding was refused by the browser before the request was sent.
	allowed := preflight(t, "https://console.example", "PUT").
		Get("Access-Control-Allow-Methods")

	for _, m := range []string{"GET", "POST", "PUT", "OPTIONS"} {
		if !strings.Contains(allowed, m) {
			t.Errorf("%s is not allowed: %q", m, allowed)
		}
	}
}

func TestEveryHeaderTheClientsSendIsAllowed(t *testing.T) {
	// Authorization and Content-Type were there. The rest are headers this
	// API defined for itself and then did not permit: an idempotency key the
	// queue depends on, the build version the office checks, and the grant
	// that says which of an account's farmers is being asked about.
	allowed := preflight(t, "https://console.example", "POST").
		Get("Access-Control-Allow-Headers")

	for _, h := range []string{
		"Authorization", "Content-Type", "Idempotency-Key", "X-Farmer-Grant",
	} {
		if !strings.Contains(allowed, h) {
			t.Errorf("%s is not allowed: %q", h, allowed)
		}
	}
}

func TestAnUnknownOriginIsToldNothing(t *testing.T) {
	h := preflight(t, "https://not-the-console.example", "POST")
	if got := h.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("an unlisted origin was allowed: %q", got)
	}
	if got := h.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("credentials offered to an unlisted origin: %q", got)
	}
}

func TestTheAnswerVariesByOrigin(t *testing.T) {
	// Without this a shared cache can hand one origin's permission to
	// another, which turns an allowlist into a wildcard with extra steps.
	h := preflight(t, "https://console.example", "POST")
	if !strings.Contains(h.Get("Vary"), "Origin") {
		t.Errorf("Vary = %q, want Origin", h.Get("Vary"))
	}
}

func TestNoWildcardOriginIsEverSent(t *testing.T) {
	// Credentials are attached, and a wildcard cannot carry them -- but more
	// to the point, a wildcard here would let any page on the internet read
	// an officer's console responses.
	h := preflight(t, "https://console.example", "POST")
	if h.Get("Access-Control-Allow-Origin") == "*" {
		t.Error("the origin was answered with a wildcard")
	}
}
