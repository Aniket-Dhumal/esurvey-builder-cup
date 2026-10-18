package sms

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The failure everybody fears here is silent: a gateway that returns 200 and
// delivers nothing, so the platform believes it told four thousand farmers
// and none of them heard. Most of what follows is about refusing to be that.

// gateway builds one against a fake provider.
//
// The query template is passed in rather than appended afterwards: the
// transport is constructed from the config at New() time and holds its own
// copy, so a later edit to the gateway's config changes nothing. The first
// run of this suite proved it by sending an empty query.
func gateway(t *testing.T, cfg Config, query string, h http.HandlerFunc) *Gateway {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	cfg.BaseURL = srv.URL + query
	if cfg.SenderID == "" {
		cfg.SenderID = "ESURVY"
	}
	if cfg.Templates == nil {
		cfg.Templates = map[string]string{"report.acknowledged": "1707161234567890123"}
	}
	g, err := New(cfg, srv.Client())
	if err != nil {
		t.Fatalf("building the gateway: %v", err)
	}
	return g
}

// ---------------------------------------------------------------------------
// DLT, which is the whole reason this is not just an HTTP POST
// ---------------------------------------------------------------------------

func TestAMessageWithNoRegisteredTemplateIsRefusedBeforeTheWire(t *testing.T) {
	// An operator rejects a message that does not match a template registered
	// against the sender header, and the aggregator still answers 200. Posting
	// it anyway means the platform records a delivery that never happened.
	var called bool
	g := gateway(t, Config{Provider: "msg91"}, "", func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = io.WriteString(w, `{"type":"success","request_id":"x"}`)
	})

	_, err := g.Send(context.Background(), "grievance.decided", "9876543210", "काही तरी")
	if !errors.Is(err, ErrNoTemplate) {
		t.Fatalf("error = %v, want ErrNoTemplate", err)
	}
	if called {
		t.Error("a message with no DLT template reached the gateway")
	}
	if !strings.Contains(err.Error(), "grievance.decided") {
		t.Errorf("the error does not name the event that needs registering: %v", err)
	}
}

func TestAMissingTemplateIsNotRetried(t *testing.T) {
	// No number of attempts registers a template, and retrying would burn the
	// attempt budget the outbox keeps for failures that recover.
	if Retryable(ErrNoTemplate) {
		t.Error("a missing template was reported as retryable")
	}
}

func TestAGatewayWithNoSenderHeaderRefusesToExist(t *testing.T) {
	// An unregistered header is rejected by every Indian operator, so a
	// deployment that forgot to set one should fail at startup rather than at
	// the first farmer.
	_, err := New(Config{Provider: "msg91", SenderID: ""}, nil)
	if err == nil {
		t.Fatal("a gateway with no sender header was built")
	}
	if !strings.Contains(err.Error(), "rejected by every Indian operator") {
		t.Errorf("the error does not say why it matters: %v", err)
	}
}

func TestTheTemplateAndHeaderReachTheProvider(t *testing.T) {
	var body, authkey string
	g := gateway(t, Config{Provider: "msg91", AuthKey: "secret"}, "",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			authkey = r.Header.Get("authkey")
			_, _ = io.WriteString(w, `{"type":"success","request_id":"req-1"}`)
		})

	ref, err := g.Send(context.Background(), "report.acknowledged",
		"9876543210", "तुमची नोंद मिळाली आहे.")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "req-1" {
		t.Errorf("reference = %q", ref)
	}
	if !strings.Contains(body, "1707161234567890123") {
		t.Errorf("the DLT template id did not reach the provider: %s", body)
	}
	if !strings.Contains(body, "919876543210") {
		t.Errorf("the number went without a country code: %s", body)
	}
	if authkey != "secret" {
		t.Errorf("the auth key did not reach the provider")
	}
}

// ---------------------------------------------------------------------------
// The 200 that means failure
// ---------------------------------------------------------------------------

func TestA200WithAnErrorBodyIsAFailure(t *testing.T) {
	// The single most common way an SMS integration lies to its operator.
	g := gateway(t, Config{Provider: "msg91"}, "", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w,
			`{"type":"error","message":"Template ID not found for this sender"}`)
	})

	_, err := g.Send(context.Background(), "report.acknowledged", "9876543210", "नोंद")
	if err == nil {
		t.Fatal("a 200 with an error body was reported as delivered")
	}
	if !errors.Is(err, ErrRejected) {
		t.Errorf("error = %v, want ErrRejected", err)
	}
	if !strings.Contains(err.Error(), "Template ID not found") {
		t.Errorf("the provider's own reason was lost: %v", err)
	}
}

func TestAGenericGatewaysPlainTextRefusalIsRead(t *testing.T) {
	// Most Indian aggregators answer 200 with a line of text. There is no
	// standard, so a refusal has to be recognised in the words they use.
	for _, answer := range []string{
		"ERROR: invalid template id",
		"Invalid Sender ID",
		"Authorization failed",
		"REJECTED - DND",
	} {
		g := gateway(t, Config{Provider: "generic"},
			"?to={to}&text={body}&tid={template}",
			func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, answer)
			})

		if _, err := g.Send(context.Background(), "report.acknowledged",
			"9876543210", "नोंद"); err == nil {
			t.Errorf("%q was read as success", answer)
		}
	}
}

func TestAGenericGatewaysAcceptanceIsKeptAsTheReference(t *testing.T) {
	g := gateway(t, Config{Provider: "generic"}, "?to={to}&text={body}",
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "OK|MSGID:558812")
		})

	ref, err := g.Send(context.Background(), "report.acknowledged", "9876543210", "नोंद")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ref, "558812") {
		t.Errorf("reference = %q; nothing can be traced to this message", ref)
	}
}

func TestTheGenericPlaceholdersAreFilledAndEscaped(t *testing.T) {
	var got string
	g := gateway(t, Config{Provider: "generic", AuthKey: "k&y"},
		"?key={key}&to={to}&sender={sender}&tid={template}&uc={unicode}&text={body}",
		func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.RawQuery
			_, _ = io.WriteString(w, "OK")
		})

	if _, err := g.Send(context.Background(), "report.acknowledged",
		"9876543210", "तुमची नोंद"); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"to=9876543210", "sender=ESURVY",
		"tid=1707161234567890123", "uc=1"} {
		if !strings.Contains(got, want) {
			t.Errorf("query is missing %s: %s", want, got)
		}
	}
	// An unescaped ampersand in the key would truncate the query and send a
	// message with no text.
	if strings.Contains(got, "key=k&y") {
		t.Errorf("the auth key was not escaped: %s", got)
	}
}

// ---------------------------------------------------------------------------
// What is worth retrying
// ---------------------------------------------------------------------------

func TestAnUnreachableGatewayIsRetryableAndARefusalIsNot(t *testing.T) {
	// The outbox holds the backoff; this only says which kind of failure it
	// is. Retrying a rejected template wastes a budget kept for outages.
	cases := map[error]bool{
		ErrUnavailable:                true,
		ErrRejected:                   false,
		ErrNoTemplate:                 false,
		ErrBadNumber:                  false,
		errors.New("connection lost"): true, // unclassified: more often a network
		nil:                           false,
	}
	for err, want := range cases {
		if got := Retryable(err); got != want {
			t.Errorf("Retryable(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestAServerErrorIsRetryableAndABadRequestIsNot(t *testing.T) {
	for status, wantRetry := range map[int]bool{
		500: true, 502: true, 503: true, 429: true,
		400: false, 401: false, 403: false, 422: false,
	} {
		g := gateway(t, Config{Provider: "msg91"}, "",
			func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"message":"no"}`)
			})

		_, err := g.Send(context.Background(), "report.acknowledged", "9876543210", "नोंद")
		if err == nil {
			t.Errorf("http %d was reported as delivered", status)
			continue
		}
		if got := Retryable(err); got != wantRetry {
			t.Errorf("http %d: retryable = %v, want %v (%v)", status, got, wantRetry, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Numbers, as people actually write them
// ---------------------------------------------------------------------------

func TestOneNumberWrittenSixWaysIsOnePerson(t *testing.T) {
	// They arrive from a registry, from a farmer typing on a phone, and from
	// a call centre. A gateway that treated these as different numbers would
	// fail five of the six.
	for _, raw := range []string{
		"9876543210",
		"09876543210",
		"+91 98765 43210",
		"0091-9876543210",
		"91 9876543210",
		"  98765-43210  ",
	} {
		got, err := NormaliseIndianMobile(raw)
		if err != nil {
			t.Errorf("%q was refused: %v", raw, err)
			continue
		}
		if got != "9876543210" {
			t.Errorf("%q normalised to %q", raw, got)
		}
	}
}

func TestSomethingThatIsNotAMobileIsRefusedWithTheReason(t *testing.T) {
	for _, raw := range []string{
		"",               // never recorded
		"12345",          // too short
		"5876543210",     // no Indian mobile begins 5
		"02025671234",    // a Pune landline
		"98765432101234", // too long
	} {
		if got, err := NormaliseIndianMobile(raw); err == nil {
			t.Errorf("%q was accepted as %q", raw, got)
		}
	}

	_, err := NormaliseIndianMobile("5876543210")
	if !strings.Contains(err.Error(), "6, 7, 8 or 9") {
		t.Errorf("the refusal does not say what a mobile looks like: %v", err)
	}
}

func TestABadNumberNeverReachesTheGateway(t *testing.T) {
	var called bool
	g := gateway(t, Config{Provider: "msg91"}, "", func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	if _, err := g.Send(context.Background(), "report.acknowledged",
		"not a number", "नोंद"); !errors.Is(err, ErrBadNumber) {
		t.Fatalf("error = %v", err)
	}
	if called {
		t.Error("an unreachable number was posted to the provider anyway")
	}
}

// ---------------------------------------------------------------------------
// Marathi
// ---------------------------------------------------------------------------

func TestAMarathiMessageIsSentAsUnicode(t *testing.T) {
	// An aggregator not told encodes it as GSM-7 and the farmer receives
	// question marks -- which is indistinguishable at this end from arrival.
	if !NeedsUnicode("तुमची नोंद मिळाली आहे.") {
		t.Error("a Devanagari message was not flagged as unicode")
	}
	if NeedsUnicode("Your report has been received.") {
		t.Error("a plain ASCII message was flagged as unicode")
	}
}

func TestSegmentsAreCountedTheWayTheBillIs(t *testing.T) {
	// Four hundred thousand farmers times one extra segment is a real number,
	// and worth knowing before a district-wide send rather than after.
	cases := []struct {
		body string
		want int
	}{
		{"", 0},
		{"short", 1},
		{strings.Repeat("a", 160), 1},
		{strings.Repeat("a", 161), 2},
		{"नोंद", 1},
		{strings.Repeat("अ", 70), 1},
		{strings.Repeat("अ", 71), 2},
		{strings.Repeat("अ", 134), 2},
		{strings.Repeat("अ", 135), 3},
	}
	for _, c := range cases {
		if got := Segments(c.body); got != c.want {
			t.Errorf("Segments(%d runes, unicode=%v) = %d, want %d",
				len([]rune(c.body)), NeedsUnicode(c.body), got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Rehearsal
// ---------------------------------------------------------------------------

func TestADryRunSendsNothingAndSaysSo(t *testing.T) {
	// The failure everyone fears before a district-wide event is a rehearsal
	// that actually messages four thousand farmers.
	var called bool
	g := gateway(t, Config{Provider: "msg91", DryRun: true}, "",
		func(w http.ResponseWriter, r *http.Request) { called = true })

	ref, err := g.Send(context.Background(), "report.acknowledged", "9876543210", "नोंद")
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("a dry run reached the provider")
	}
	if !strings.HasPrefix(ref, "dryrun-") {
		t.Errorf("reference = %q; it must not be mistakable for a real one", ref)
	}
	if !strings.Contains(g.Name(), "dry run") {
		t.Errorf("the provider name does not say it is a rehearsal: %s", g.Name())
	}
}

// ---------------------------------------------------------------------------
// What ends up in a log
// ---------------------------------------------------------------------------

func TestANumberIsReducedToItsLastFourForALog(t *testing.T) {
	// A log line is read by more people than a database row and gets pasted
	// into tickets.
	got := redact("9876543210")
	if strings.Contains(got, "987654") {
		t.Errorf("redact left the number readable: %s", got)
	}
	if !strings.HasSuffix(got, "3210") {
		t.Errorf("redact left nothing to identify it by: %s", got)
	}
	if redact("12") == "" {
		t.Error("redact returned nothing for a short input")
	}
}

func TestAnHTMLErrorPageDoesNotBecomeTheStoredError(t *testing.T) {
	// A proxy in front of an aggregator returns a kilobyte of markup, and it
	// would otherwise land in last_error on every failed notification.
	g := gateway(t, Config{Provider: "generic"}, "?to={to}&text={body}",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w,
				"<html><head><title>502</title></head><body>"+
					strings.Repeat("gateway error ", 200)+"</body></html>")
		})

	_, err := g.Send(context.Background(), "report.acknowledged", "9876543210", "नोंद")
	if err == nil {
		t.Fatal("a 502 was reported as delivered")
	}
	if len(err.Error()) > 400 {
		t.Errorf("the stored error is %d characters", len(err.Error()))
	}
}

func TestAnEmptyMessageIsRefused(t *testing.T) {
	g := gateway(t, Config{Provider: "msg91"}, "",
		func(w http.ResponseWriter, r *http.Request) {
			t.Error("an empty message reached the provider")
		})
	if _, err := g.Send(context.Background(), "report.acknowledged",
		"9876543210", "   "); err == nil {
		t.Fatal("an empty message was accepted")
	}
}

func TestAnUnknownProviderIsRefusedAtStartup(t *testing.T) {
	if _, err := New(Config{Provider: "carrier-pigeon", SenderID: "ESURVY"}, nil); err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	if _, err := New(Config{SenderID: "ESURVY"}, nil); err == nil {
		t.Fatal("a gateway with no provider was accepted")
	}
}
