package weather

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The archive answers 502 with an HTML error page, intermittently.
//
// It degrades correctly — the source is reported absent and the assessment
// proceeds — and that is worse than it sounds. Weather is the record of
// whether the calamity happened at all, the packet is stored with the
// assessment, and an appeal next year reads the gap rather than the outage.

const dayJSON = `{"daily":{"time":["2026-09-01"],"temperature_2m_max":[31.2],` +
	`"temperature_2m_min":[22.1],"precipitation_sum":[48.0],` +
	`"precipitation_hours":[3],"wind_speed_10m_max":[22],` +
	`"wind_gusts_10m_max":[61],"weather_code":[96]}}`

func clientFor(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return &Client{ArchiveURL: srv.URL, HTTP: srv.Client()}
}

func TestATransient502IsRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			// Exactly what the archive does: an HTML error page, not JSON.
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(dayJSON))
	}))
	defer srv.Close()

	s, err := clientFor(t, srv).fetch(context.Background(), 18.0, 75.0,
		"2026-09-01", "2026-09-01")
	if err != nil {
		t.Fatalf("a single 502 was not retried: %v", err)
	}
	if len(s.Days) != 1 || s.Days[0].RainMM != 48 {
		t.Fatalf("the retried response did not parse: %+v", s.Days)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
}

// A refusal is not a bad moment. Asking again gets the same answer more slowly,
// out of a budget shared with radar, the registry and the panel.
func TestARefusalIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":true,"reason":"start_date is out of range"}`))
	}))
	defer srv.Close()

	_, err := clientFor(t, srv).fetch(context.Background(), 18.0, 75.0,
		"1850-01-01", "1850-01-02")
	if err == nil {
		t.Fatal("a refusal should be an error")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("the archive's own words were lost: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1 — a refusal must not be retried", got)
	}
}

// Sustained failure still fails, and says how hard it tried.
func TestASustainedOutageGivesUp(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, err := clientFor(t, srv).fetch(context.Background(), 18.0, 75.0,
		"2026-09-01", "2026-09-01")
	if err == nil {
		t.Fatal("a sustained outage should be an error")
	}
	if !strings.Contains(err.Error(), "attempts") {
		t.Errorf("the error does not say it retried: %v", err)
	}
	if got := calls.Load(); got != fetchAttempts {
		t.Fatalf("attempts = %d, want %d", got, fetchAttempts)
	}
}

// The retry must not outlive the caller. The whole evidence assembly runs
// under a budget, and a retry that ignores it spends another source's time.
func TestRetriesStopWhenTheCallerGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := clientFor(t, srv).fetch(ctx, 18.0, 75.0,
		"2026-09-01", "2026-09-01"); err == nil {
		t.Fatal("expected an error")
	}
	// Well under the full backoff schedule, which is 400ms + 800ms.
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Fatalf("kept retrying for %v after the caller gave up", d)
	}
}
