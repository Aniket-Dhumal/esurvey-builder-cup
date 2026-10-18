package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The client is a thin thing, and the tests are about the two places where
// being thin is not the same as being harmless: the predicate that decides
// whether a damage figure is allowed at all, and what a caller can tell about
// a failure. Everything here runs against an httptest server, so the suite
// needs no agent and no key.

// ---------------------------------------------------------------------------
// The gate on the damage figure
// ---------------------------------------------------------------------------

func TestOnlyGroundImageryCountsAsGroundImagery(t *testing.T) {
	// This predicate decides whether the panel may state a number. A
	// colourised NDVI overlay looks like a picture of a field and is not one:
	// it is a satellite index rendered in green and red. Letting it through
	// would mean a damage percentage derived from no direct observation, on a
	// record that a farmer's compensation is assessed from.
	cases := []struct {
		name   string
		images []Image
		want   bool
	}{
		{"nothing at all", nil, false},
		{"an empty set", []Image{}, false},
		{"NDVI only", []Image{{Kind: ImageNDVIMid}, {Kind: ImageNDVILast}}, false},
		{"terrain only", []Image{{Kind: ImageTerrainSlope}, {Kind: ImageTerrainAspect}}, false},
		{"every satellite product there is", []Image{
			{Kind: ImageNDVIMid}, {Kind: ImageNDVILast},
			{Kind: ImageTerrainSlope}, {Kind: ImageTerrainAspect},
		}, false},
		{"a field photograph", []Image{{Kind: ImageFieldPhoto}}, true},
		{"a survey keyframe", []Image{{Kind: ImageSurveyKeyfr}}, true},
		{"one photograph among satellite products", []Image{
			{Kind: ImageNDVIMid}, {Kind: ImageFieldPhoto}, {Kind: ImageTerrainSlope},
		}, true},
		{"an unknown kind is not ground truth", []Image{{Kind: "screenshot"}}, false},
		{"an empty kind is not ground truth", []Image{{Kind: ""}}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := HasGroundImagery(c.images); got != c.want {
				t.Errorf("HasGroundImagery = %v, want %v", got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Telling failures apart
// ---------------------------------------------------------------------------

func TestAnUnconfiguredAgentIsDistinguishableFromABrokenOne(t *testing.T) {
	// One is an operator action -- a key that was never set -- and the other
	// is an incident. Reporting both as "assessment failed" sends somebody
	// reading logs for a bug that is a missing environment variable.
	cases := []struct {
		status int
		want   bool
	}{
		{http.StatusServiceUnavailable, true},
		{http.StatusInternalServerError, false},
		{http.StatusBadRequest, false},
		{http.StatusBadGateway, false},
		{http.StatusGatewayTimeout, false},
	}

	for _, c := range cases {
		e := &Error{HTTPStatus: c.status, Detail: "detail"}
		if got := e.Unavailable(); got != c.want {
			t.Errorf("http %d: Unavailable = %v, want %v", c.status, got, c.want)
		}
	}
}

func TestAFailureCarriesBothTheStatusAndWhatTheAgentSaid(t *testing.T) {
	e := &Error{HTTPStatus: 503, Detail: "GEMINI_API_KEY is not set"}
	msg := e.Error()
	if !strings.Contains(msg, "GEMINI_API_KEY is not set") {
		t.Errorf("the message drops the agent's own reason: %q", msg)
	}
	if !strings.Contains(msg, "503") {
		t.Errorf("the message drops the status: %q", msg)
	}
}

// ---------------------------------------------------------------------------
// Assess
// ---------------------------------------------------------------------------

// agent stands in for the assessment service.
func agent(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	c := New(srv.URL)
	// Short, so a hung handler fails the test rather than the package.
	c.HTTP.Timeout = 10 * time.Second
	return c
}

func TestAssessSendsTheRequestAsJSONToTheAssessPath(t *testing.T) {
	var gotPath, gotType, gotMethod string
	var body AssessRequest

	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotType, gotMethod = r.URL.Path, r.Header.Get("Content-Type"), r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_ = json.NewEncoder(w).Encode(AssessResponse{AssessmentID: "a-1"})
	})

	_, err := c.Assess(context.Background(), AssessRequest{AssessmentID: "a-1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/v1/assess" {
		t.Errorf("path = %s", gotPath)
	}
	if gotType != "application/json" {
		t.Errorf("content type = %q", gotType)
	}
	if body.AssessmentID != "a-1" {
		t.Errorf("the request body did not arrive: %+v", body)
	}
}

func TestAnAssessmentWithNoNumberIsNotAnAssessmentWithZero(t *testing.T) {
	// DamagePercent is a pointer for exactly this. The expected outcome
	// without ground imagery is "no figure", and decoding that as 0.0 would
	// turn a refusal to guess into a finding of no damage.
	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{
			"assessment_id": "a-2",
			"result": {
				"damage_percent": null,
				"insufficient_evidence": true,
				"severity": "unknown",
				"confidence": 0.0,
				"reasoning": "no ground imagery was supplied"
			}
		}`)
	})

	out, err := c.Assess(context.Background(), AssessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.DamagePercent != nil {
		t.Errorf("a missing figure decoded as %v", *out.Result.DamagePercent)
	}
	if !out.Result.InsufficientEvidence {
		t.Error("the refusal to quantify did not survive decoding")
	}
}

func TestAZeroDamageFindingIsKept(t *testing.T) {
	// The other half of the same rule: a panel that looked and found no damage
	// said something, and it must not be flattened into "no figure".
	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w,
			`{"assessment_id":"a-3","result":{"damage_percent":0.0,"severity":"none"}}`)
	})

	out, err := c.Assess(context.Background(), AssessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.DamagePercent == nil {
		t.Fatal("a finding of zero damage decoded as no finding")
	}
	if *out.Result.DamagePercent != 0 {
		t.Errorf("damage = %v, want 0", *out.Result.DamagePercent)
	}
}

func TestAShortHandedPanelSaysSoAndSaysWhy(t *testing.T) {
	// An officer reading an assessment needs to know it was made without the
	// weather check. A degraded run that looks identical to a full one is the
	// failure this field exists to prevent.
	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(AssessResponse{
			AssessmentID:   "a-4",
			Degraded:       true,
			DegradedReason: "the weather specialist did not run",
			Specialists: []Specialist{
				{Agent: "weather", Ran: false, Error: "open-meteo timed out"},
				{Agent: "vision", Ran: true, Verdict: "damage consistent with hail"},
			},
		})
	})

	out, err := c.Assess(context.Background(), AssessRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Degraded || out.DegradedReason == "" {
		t.Error("a short-handed panel came back looking complete")
	}
	if len(out.Specialists) != 2 {
		t.Fatalf("specialists = %d, want 2", len(out.Specialists))
	}
	if out.Specialists[0].Ran {
		t.Error("the specialist that failed is reported as having run")
	}
	if out.Specialists[0].Error == "" {
		t.Error("the specialist that failed carries no reason")
	}
}

func TestAnErrorStatusBecomesAnErrorTheCallerCanInspect(t *testing.T) {
	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"detail":"GEMINI_API_KEY is not set"}`)
	})

	_, err := c.Assess(context.Background(), AssessRequest{})
	if err == nil {
		t.Fatal("a 503 came back as success")
	}

	var aiErr *Error
	if !errors.As(err, &aiErr) {
		t.Fatalf("error is %T, want *ai.Error", err)
	}
	if !aiErr.Unavailable() {
		t.Error("a 503 is not reported as unavailable")
	}
	if !strings.Contains(aiErr.Detail, "GEMINI_API_KEY") {
		t.Errorf("the agent's reason was lost: %q", aiErr.Detail)
	}
}

func TestANonJSONFailureBodyStillReachesTheCaller(t *testing.T) {
	// A proxy or load balancer in front of the agent returns HTML. Dropping it
	// leaves an operator with a status code and nothing to search for.
	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html><body>502 Bad Gateway</body></html>")
	})

	_, err := c.Assess(context.Background(), AssessRequest{})
	var aiErr *Error
	if !errors.As(err, &aiErr) {
		t.Fatalf("error is %T, want *ai.Error", err)
	}
	if !strings.Contains(aiErr.Detail, "Bad Gateway") {
		t.Errorf("the body was dropped: %q", aiErr.Detail)
	}
}

func TestAMalformedSuccessBodyIsAnError(t *testing.T) {
	// 200 with a truncated body is what a killed agent looks like. Returning
	// a zero-valued assessment would record "no damage" for it.
	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"assessment_id": "a-5", "result": {`)
	})

	out, err := c.Assess(context.Background(), AssessRequest{})
	if err == nil {
		t.Fatalf("a truncated body decoded into %+v", out)
	}
	if !strings.Contains(err.Error(), "decoding") {
		t.Errorf("error = %v, want it to name decoding", err)
	}
}

func TestACancelledContextStopsTheRequest(t *testing.T) {
	// The assessment runs for tens of seconds. When the caller's request is
	// gone, Assess must return rather than hold the caller for the client's
	// three-minute timeout.
	//
	// The assertion is client-side on purpose. Whether the server notices the
	// disconnect promptly is net/http's business and varies by platform; what
	// this package owns is that the context reaches the request.
	started := make(chan struct{})
	release := make(chan struct{})

	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	// Registered after agent(), so it runs before httptest's Close, which
	// waits for outstanding handlers.
	t.Cleanup(func() { close(release) })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := c.Assess(ctx, AssessRequest{})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled assessment returned success")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want it to wrap context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Assess did not return after its context was cancelled")
	}
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func TestHealthReportsWhatTheAgentSaysAboutItself(t *testing.T) {
	// The model and prompt version are read from here into the assessment
	// record, so that a figure can be traced to what produced it.
	c := agent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("health asked for %s", r.URL.Path)
		}
		_, _ = io.WriteString(w,
			`{"status":"ok","model":"gemini-3.1-pro-preview","prompt_version":"v3"}`)
	})

	out, err := c.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if out["model"] != "gemini-3.1-pro-preview" {
		t.Errorf("model = %v", out["model"])
	}
	if out["prompt_version"] != "v3" {
		t.Errorf("prompt version = %v", out["prompt_version"])
	}
}

func TestAnUnreachableAgentIsReportedAsUnreachable(t *testing.T) {
	c := New("http://127.0.0.1:1")
	c.HTTP.Timeout = 2 * time.Second

	if _, err := c.Health(context.Background()); err == nil {
		t.Fatal("an unreachable agent reported healthy")
	} else if !strings.Contains(err.Error(), "reaching ai agent") {
		t.Errorf("error = %v, want it to say the agent was unreachable", err)
	}
}
