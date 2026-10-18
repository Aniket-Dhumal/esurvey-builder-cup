package api

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Why a request was refused, on the request's own log line.
//
// Found on a handset: a claim walked and filmed at all four points came back
// 400. The phone keeps a friendly translation, the request log kept only the
// status, and so the actual reason -- "documents_promised is not satisfied" --
// was recorded nowhere and had to be recovered by replaying the request.

func TestARefusalsReasonReachesTheRequestLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	h := logging(log, 0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusBadRequest, "INVALID_REPORT",
			"invalid report: documents_promised is not satisfied")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/reports", nil))

	line := buf.String()
	if !strings.Contains(line, "INVALID_REPORT") ||
		!strings.Contains(line, "documents_promised is not satisfied") {
		t.Errorf("the reason is not on the request line:\n%s", line)
	}
}

func TestTheReasonSurvivesTheIdempotencyWrapper(t *testing.T) {
	// Every route the farmer's queue writes to is idempotent, so its refusals
	// pass through that recorder first. If the reason stopped there, the
	// refusals a handset cannot explain would be exactly the ones left out.
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	h := logging(log, 0, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		wrapped := &recorder{ResponseWriter: w}
		writeError(wrapped, http.StatusUnprocessableEntity, "NOT_IN_EVENT_AREA",
			"village is not in any taluka the event covers")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/v1/reports", nil))

	if !strings.Contains(buf.String(), "NOT_IN_EVENT_AREA") {
		t.Errorf("the reason stopped at the idempotency recorder:\n%s", buf.String())
	}
}
