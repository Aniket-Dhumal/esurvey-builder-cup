package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Officers sign in with their email address; an old username is refused on
// its shape, before any account is looked up.
func TestAnOfficerSignsInWithTheirEmailAddress(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	for _, username := range []string{"admin", "dm.solapur", "verify.admin@esurvey.local"} {
		body := `{"username":"` + username + `","password":"irrelevant-password"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/officer/login",
			strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		s.officerLogin(rec, req)

		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "INVALID_EMAIL") {
			t.Errorf("%q: %d %s, want 400 INVALID_EMAIL", username, rec.Code, rec.Body)
		}
	}
}
