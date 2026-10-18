package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The route table, built.
//
// Go's ServeMux panics at registration when two patterns conflict, so a
// duplicate route is not a subtle bug: the whole service fails to start. It
// happened here -- a second GET on the same parcel boundary path -- and every
// other test passed, because none of them builds the mux.
//
// This one does nothing but build it. That is the point.

func TestTheRouteTableCanBeBuilt(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("registering the routes panicked, so the service would "+
				"not start: %v", r)
		}
	}()

	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if s.Routes() == nil {
		t.Fatal("no handler was returned")
	}
}

func TestEveryRouteRefusesAnAnonymousCaller(t *testing.T) {
	// A route registered without s.require is reachable by anybody. The
	// mistake is one word wide and invisible in review, so it is checked
	// here against the real table rather than trusted.
	//
	// The public ones are named: everything else must answer 401.
	public := map[string]bool{
		"/healthz": true, "/readyz": true, "/metrics": true,
		"/api/v1/auth/officer/login": true,
		"/api/v1/auth/farmer/start":  true,
		"/api/v1/auth/farmer/verify": true,
		"/api/v1/auth/refresh":       true,
		"/api/v1/auth/logout":        true,
	}

	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	h := s.Routes()

	// A sample across the surface, including everything added recently.
	for _, path := range []string{
		"/api/v1/assessments",
		"/api/v1/farmers",
		"/api/v1/reports",
		"/api/v1/parcels/X/boundary",
		"/api/v1/parcels/X/boundary/confirm",
		"/api/v1/reports/X/measurements",
		"/api/v1/expected-stand",
		"/api/v1/admin/users",
		"/api/v1/admin/audit",
	} {
		if public[path] {
			continue
		}
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			req := httptest.NewRequest(method, path, strings.NewReader("{}"))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			switch rec.Code {
			case http.StatusUnauthorized:
				// What it should be.
			case http.StatusMethodNotAllowed, http.StatusNotFound:
				// This method is not registered on this path, which is fine.
			default:
				t.Errorf("%s %s answered %d to an anonymous caller, want 401",
					method, path, rec.Code)
			}
		}
	}
}
