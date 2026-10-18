package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Who may read the metrics.
//
// The endpoint carries no farmer data -- its labels are route patterns and
// never paths -- but it publishes how many requests a district is handling,
// which routes fail and how slowly. That is reconnaissance, and it was
// readable by anybody who could reach the port.

func scrape(t *testing.T, s *Server, from, auth string) int {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.RemoteAddr = from
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	s.metrics(w, r)
	return w.Code
}

func TestWithNoTokenOnlyLoopbackMayScrape(t *testing.T) {
	s := &Server{Metrics: NewMetrics()}

	for _, addr := range []string{"127.0.0.1:51000", "[::1]:51000"} {
		if code := scrape(t, s, addr, ""); code != http.StatusOK {
			t.Errorf("loopback %s got %d; a sidecar scraper has to work", addr, code)
		}
	}
	for _, addr := range []string{"203.0.113.9:51000", "10.1.2.3:51000"} {
		if code := scrape(t, s, addr, ""); code == http.StatusOK {
			t.Errorf("%s read the metrics with no token", addr)
		}
	}
}

func TestAForwardedHeaderCannotClaimToBeLoopback(t *testing.T) {
	// The whole of the control. An address a caller supplies is not an
	// address, and clientIP honours X-Forwarded-For -- which is why this
	// check reads the connection instead.
	s := &Server{Metrics: NewMetrics(), TrustedProxyHops: 1}

	r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.RemoteAddr = "203.0.113.9:51000"
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	w := httptest.NewRecorder()
	s.metrics(w, r)

	if w.Code == http.StatusOK {
		t.Error("a caller read the metrics by claiming to be loopback in a header")
	}
}

func TestWithATokenTheRightOneReadsAndOthersDoNot(t *testing.T) {
	s := &Server{Metrics: NewMetrics(), MetricsToken: "s3cret-scrape-token"}

	if code := scrape(t, s, "203.0.113.9:51000", "Bearer s3cret-scrape-token"); code != http.StatusOK {
		t.Errorf("the configured scraper got %d", code)
	}
	for _, auth := range []string{
		"", "Bearer ", "Bearer wrong", "Bearer s3cret-scrape-toke",
		"s3cret-scrape-token",
	} {
		if code := scrape(t, s, "203.0.113.9:51000", auth); code == http.StatusOK {
			t.Errorf("%q read the metrics", auth)
		}
	}
}

func TestWithATokenLoopbackStillNeedsIt(t *testing.T) {
	// Once a token is configured it is the rule. Leaving loopback open as
	// well would mean anything that got a shell on the box, or any process in
	// the same pod, could read without one.
	s := &Server{Metrics: NewMetrics(), MetricsToken: "s3cret"}
	if code := scrape(t, s, "127.0.0.1:51000", ""); code == http.StatusOK {
		t.Error("loopback bypassed the configured token")
	}
}

func TestRefusalLooksLikeAbsence(t *testing.T) {
	// 404 rather than 403. A scraper that is configured knows where this is;
	// to anybody else the endpoint may as well not exist, and a 403 confirms
	// it does.
	s := &Server{Metrics: NewMetrics(), MetricsToken: "s3cret"}
	if code := scrape(t, s, "203.0.113.9:51000", ""); code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", code)
	}
}
