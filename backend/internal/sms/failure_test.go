package sms

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/auth"
)

// What a farmer is told when the code cannot be sent.
//
// Two answers, and the difference decides what they do next. "The gateway is
// busy" is worth another press in a minute. "No DLT template is registered"
// is not worth another press this season, and telling them to try again is
// telling them to stand in a field pressing a button that cannot work.

func otpFor(t *testing.T, h http.HandlerFunc) OTP {
	t.Helper()
	// With a template registered for the code message: without one the
	// gateway refuses before the transport is reached, which is its own
	// (tested) behaviour and not what these tests are about.
	g := gateway(t, Config{
		Provider:  "enterprise",
		Username:  "esurvey.trans",
		AuthKey:   "gateway-password",
		Templates: map[string]string{OTPEventCode: "1707161234567890123"},
	}, "", h)
	return OTP{Gateway: g, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestABusyGatewayIsWorthAnotherPress(t *testing.T) {
	otp := otpFor(t, func(w http.ResponseWriter, r *http.Request) {
		// A zero balance: somebody tops the account up and the next one goes.
		_, _ = io.WriteString(w, `{"statusCode":6001,"description":"Insufficient Balance!"}`)
	})

	err := otp.Send(t.Context(), "+919876543210", "123456")
	if !errors.Is(err, auth.ErrChannelDown) {
		t.Fatalf("a passing failure was not reported as one: %v", err)
	}
	if errors.Is(err, auth.ErrUndeliverable) {
		t.Fatal("a farmer would be told to give up on something that will work")
	}
}

func TestAMissingTemplateIsNotWorthAnotherPress(t *testing.T) {
	otp := otpFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"statusCode":7001,"description":"DLT_Content_ID not found"}`)
	})

	err := otp.Send(t.Context(), "+919876543210", "123456")
	if !errors.Is(err, auth.ErrUndeliverable) {
		t.Fatalf("a permanent fault was reported as a passing one: %v", err)
	}
}

func TestAnUnreachableGatewayIsWorthAnotherPress(t *testing.T) {
	otp := otpFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})

	err := otp.Send(t.Context(), "+919876543210", "123456")
	if !errors.Is(err, auth.ErrChannelDown) {
		t.Fatalf("an outage was not reported as a passing failure: %v", err)
	}
}

func TestTheCodeIsNeverInTheFailure(t *testing.T) {
	otp := otpFor(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"statusCode":2070,"description":"Authentication failure"}`)
	})

	// A one-time code in an error string ends up in a log, a bug report and a
	// screenshot. The message a farmer sees carries none of it either.
	err := otp.Send(t.Context(), "+919876543210", "424242")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if got := err.Error(); strings.Contains(got, "424242") {
		t.Fatalf("the code is in the error: %q", got)
	}
}
