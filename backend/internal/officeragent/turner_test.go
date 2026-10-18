package officeragent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The contract between this service and the AI service.
//
// Both ends were tested against their own idea of it and neither against the other,
// which is how the header name came to be wrong: this sent X-Esurvey-Caller, the AI
// service reads Authorization or X-API-Key, and anything else is no credential. Every
// question returned 401 and reached the officer as "the assistant could not be
// reached" -- a failure that looks like the model being down and is a header.

func TestTheTokenIsSentAsBearer(t *testing.T) {
	var gotAuth, gotType string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotType = r.Header.Get("Content-Type")
			_ = json.NewEncoder(w).Encode(TurnResponse{Text: "ok", Model: "m"})
		}))
	defer srv.Close()

	turner := NewHTTPTurner(srv.URL, "secret-token")
	if _, err := turner.Turn(context.Background(), TurnRequest{
		SystemInstruction: "be brief",
	}); err != nil {
		t.Fatal(err)
	}

	if gotAuth != "Bearer secret-token" {
		t.Fatalf("Authorization %q; the AI service reads Bearer and treats "+
			"anything else as no credential", gotAuth)
	}
	if gotType != "application/json" {
		t.Fatalf("Content-Type %q", gotType)
	}
}

func TestTheRequestGoesToTheTurnPath(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			path = r.URL.Path
			_ = json.NewEncoder(w).Encode(TurnResponse{Model: "m"})
		}))
	defer srv.Close()

	if _, err := NewHTTPTurner(srv.URL, "t").
		Turn(context.Background(), TurnRequest{}); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/agent/turn" {
		t.Fatalf("path %q", path)
	}
}

func TestNoTokenSendsNoAuthorizationHeader(t *testing.T) {
	// Development, where the service is configured with no caller token and
	// require_caller lets everything through. Sending "Bearer " with nothing after it
	// would fail the constant-time compare rather than skipping the check.
	var had bool
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, had = r.Header["Authorization"]
			_ = json.NewEncoder(w).Encode(TurnResponse{Model: "m"})
		}))
	defer srv.Close()

	if _, err := NewHTTPTurner(srv.URL, "").
		Turn(context.Background(), TurnRequest{}); err != nil {
		t.Fatal(err)
	}
	if had {
		t.Fatal("an empty token was sent as a credential")
	}
}

func TestAnErrorStatusDoesNotQuoteTheBodyBack(t *testing.T) {
	// The body can carry the prompt, and the prompt can carry the claim. An error
	// string travels into logs and screens that the claim itself does not.
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"detail":"farmer 99900000002 not found"}`))
		}))
	defer srv.Close()

	_, err := NewHTTPTurner(srv.URL, "t").Turn(context.Background(), TurnRequest{})
	if err == nil {
		t.Fatal("a 500 was not reported as an error")
	}
	if got := err.Error(); got == "" || strings.Contains(got, "99900000002") {
		t.Fatalf("the error quoted the body: %q", got)
	}
}
