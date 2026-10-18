package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A metrics endpoint is usually the least-guarded surface in a deployment.
// What it must never carry is anything that identifies a farmer, a report or
// a village -- so the two things worth testing are the labels and the ceiling
// on how many of them there can be.

func TestLatencyIsBucketedCumulatively(t *testing.T) {
	// Prometheus histogram buckets are cumulative: a request in the 10 ms
	// bucket is also in every bucket above it. Getting this wrong produces a
	// histogram that looks right and computes wrong quantiles.
	m := NewMetrics()
	m.observe("GET", "/api/v1/reports", 200, 30*time.Millisecond)

	k := routeKey{method: "GET", route: "/api/v1/reports"}
	for i, edge := range latencyBuckets {
		want := uint64(0)
		if edge >= 0.03 {
			want = 1
		}
		if got := m.buckets[k][i]; got != want {
			t.Errorf("bucket le=%g = %d, want %d", edge, got, want)
		}
	}
	if m.counts[k] != 1 {
		t.Errorf("count = %d, want 1", m.counts[k])
	}
}

func TestRouteCardinalityIsBounded(t *testing.T) {
	// An unmatched path becomes a label. Without a ceiling, a scanner walking
	// random URLs is an unbounded map in a long-running process.
	m := NewMetrics()
	for i := 0; i < maxTrackedRoutes+50; i++ {
		m.observe("GET", "/x/"+strings.Repeat("a", i%40)+string(rune('a'+i%26))+
			string(rune('0'+i%10)), 404, time.Millisecond)
	}

	if len(m.requests) > maxTrackedRoutes {
		t.Errorf("tracked %d routes, ceiling is %d", len(m.requests), maxTrackedRoutes)
	}
	if m.truncated == 0 {
		t.Error("requests were dropped without being counted")
	}
}

func TestExpositionCarriesNoIdentifiers(t *testing.T) {
	// The route template, never the path. Labelling by path would put a
	// report id -- and by extension a farmer -- into a surface that is often
	// scraped without authentication.
	s := &Server{Metrics: NewMetrics()}
	s.Metrics.observe("GET", "/api/v1/reports/{reportID}", 200, 5*time.Millisecond)

	rec := httptest.NewRecorder()
	s.metrics(rec, fromLoopback())

	body := rec.Body.String()
	if !strings.Contains(body, `route="/api/v1/reports/{reportID}"`) {
		t.Errorf("the templated route is missing:\n%s", body)
	}
	for _, leak := range []string{"farmer", "village", "aadhaar", "mobile"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Errorf("exposition mentions %q, which it must not", leak)
		}
	}
}

func TestExpositionIsWellFormed(t *testing.T) {
	s := &Server{Metrics: NewMetrics()}
	s.Metrics.observe("POST", "/api/v1/reports", 201, 12*time.Millisecond)

	rec := httptest.NewRecorder()
	s.metrics(rec, fromLoopback())

	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE esurvey_http_requests_total counter",
		"# TYPE esurvey_http_request_duration_seconds histogram",
		`esurvey_http_request_duration_seconds_bucket{method="POST",route="/api/v1/reports",le="+Inf"} 1`,
		"esurvey_http_request_duration_seconds_count",
		"esurvey_uptime_seconds",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q from:\n%s", want, body)
		}
	}

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestMetricsAreOptional(t *testing.T) {
	// A deployment without them should not 500; the endpoint simply is not
	// there, and instrument becomes a pass-through.
	s := &Server{}
	rec := httptest.NewRecorder()
	s.metrics(rec, fromLoopback())
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}

	called := false
	h := s.instrument(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if !called {
		t.Error("instrument dropped the request when metrics were disabled")
	}
}

// fromLoopback is a scrape from the machine itself.
//
// With no token configured the endpoint answers only to loopback: the
// exposition carries no farmer data, but it does publish how many requests a
// district is handling and which routes fail, and that was readable by anybody
// who could reach the port. See Server.mayScrape.
func fromLoopback() *http.Request {
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.RemoteAddr = "127.0.0.1:51000"
	return r
}
