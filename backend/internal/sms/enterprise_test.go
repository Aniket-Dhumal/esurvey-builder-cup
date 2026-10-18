package sms

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The enterprise gateway, whose refusals arrive with HTTP 200.
//
// This provider answers a rejection the same way it answers an acceptance --
// status 200, a JSON body -- and puts the verdict in a statusCode field. Three
// of its documented refusals ("Insufficient Balance!", "DLT_Content_ID not
// found", "Sender [x] doesn't exist") contain none of the words the generic
// transport looks for, so a generic integration reports all three as sent.
// That is what these tests are here to prevent.

func enterpriseGateway(t *testing.T, h http.HandlerFunc) *Gateway {
	t.Helper()
	return gateway(t, Config{
		Provider: "enterprise",
		Username: "esurvey.trans",
		AuthKey:  "gateway-password",
		// The registered principal entity. Without it on the wire the operator
		// drops the message and the aggregator still reports it accepted.
		PEID: "1701171234567890123",
	}, "", h)
}

func TestTheGatewaySeesAnAcceptance(t *testing.T) {
	g := enterpriseGateway(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"transactionId":205538399,`+
			`"state":"SUBMIT_ACCEPTED","statusCode":200,`+
			`"description":"Message accepted successfully","pdu":1}`)
	})

	ref, err := g.Send(t.Context(), "report.acknowledged", "9876543210", "Hello")
	if err != nil {
		t.Fatal(err)
	}
	if ref != "205538399" {
		t.Fatalf("the provider's reference was not kept: %q", ref)
	}
}

func TestARefusalWithNoAngryWordsInItIsStillARefusal(t *testing.T) {
	// Every one of these arrives as HTTP 200 and would pass a keyword scan.
	for _, c := range []struct {
		name, body string
		retryable  bool
	}{
		{"no credit", `{"statusCode":6001,"description":"Insufficient Balance!"}`, true},
		{"no template", `{"statusCode":7001,"description":"DLT_Content_ID not found"}`, false},
		{"unknown header", `{"statusCode":2051,"description":"Sender [ESURVY] doesn't exist"}`, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := enterpriseGateway(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, c.body)
			})

			_, err := g.Send(t.Context(), "report.acknowledged", "9876543210", "Hello")
			if err == nil {
				t.Fatal("a refusal was read as a sent message")
			}
			// A zero balance is worth retrying: somebody tops the account up
			// and the queued message goes. A missing template is not.
			if got := errors.Is(err, ErrUnavailable); got != c.retryable {
				t.Fatalf("retryable=%v, want %v (%v)", got, c.retryable, err)
			}
		})
	}
}

func TestTheMessageCarriesEverythingTheOperatorNeeds(t *testing.T) {
	// The request that actually delivers, as a query.
	//
	// This test used to assert the opposite -- that the password travelled in
	// an Authorization header and never in the URL -- because the JSON
	// endpoint takes it that way and that is the better place for a
	// credential. The JSON endpoint does not deliver: a message posted to it
	// came back SUBMIT_ACCEPTED with a transaction id and never reached the
	// handset. The GET form arrives, so the GET form is what is sent, and the
	// cost is that the password reaches the provider's access log. It is
	// inside TLS, so it is not on the wire in the clear.
	var q url.Values
	g := enterpriseGateway(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = io.WriteString(w, `{"transactionId":1,"state":"SUBMIT_ACCEPTED","statusCode":200}`)
	})

	if _, err := g.Send(t.Context(), "report.acknowledged", "9876543210", "Hello"); err != nil {
		t.Fatal(err)
	}

	// Each of these is a reason an operator silently drops a message while the
	// aggregator still answers SUBMIT_ACCEPTED, which is how this integration
	// spent a day looking like it worked.
	for _, c := range []struct{ key, want string }{
		{"username", "esurvey.trans"},
		{"password", "gateway-password"},
		{"from", "ESURVY"},
		{"to", "919876543210"},
		{"text", "Hello"},
		{"dltContentId", "1707161234567890123"},
		{"dltPeid", "1701171234567890123"},
	} {
		if got := q.Get(c.key); got != c.want {
			t.Errorf("%s = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestAMarathiCodeIsFlaggedUnicodeToThisProvider(t *testing.T) {
	var q url.Values
	g := enterpriseGateway(t, func(w http.ResponseWriter, r *http.Request) {
		q = r.URL.Query()
		_, _ = io.WriteString(w, `{"transactionId":2,"state":"SUBMIT_ACCEPTED","statusCode":200}`)
	})

	// The one-time code message, as sms.OTP builds it.
	const marathi = "eSurvey: 123456 हा तुमचा एक-वेळ कोड आहे."
	if _, err := g.Send(t.Context(), "report.acknowledged", "9876543210", marathi); err != nil {
		t.Fatal(err)
	}
	if q.Get("unicode") != "true" {
		t.Fatalf("unicode = %q; a Marathi message sent as Latin shows the "+
			"handset mojibake", q.Get("unicode"))
	}
	// And the text survives the round trip rather than being mangled by the
	// encoding of the query itself.
	if q.Get("text") != marathi {
		t.Errorf("the message text arrived as %q", q.Get("text"))
	}
}

func TestBadCredentialsAreNotRetriedForEver(t *testing.T) {
	g := enterpriseGateway(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"statusCode":2070,"description":"Authentication failure"}`)
	})

	_, err := g.Send(t.Context(), "report.acknowledged", "9876543210", "Hello")
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("a wrong password should not be retried for ever: %v", err)
	}
}

func TestAnEnterpriseGatewayNeedsItsPair(t *testing.T) {
	_, err := New(Config{
		Provider:  "enterprise",
		BaseURL:   "https://gateway.invalid/fe/api/v1/message",
		SenderID:  "ESURVY",
		Templates: map[string]string{"auth.otp": "1707161234567890123"},
		Username:  "",
		AuthKey:   "",
	}, nil)
	// New builds it; the refusal is at send time, where the message names
	// what is missing rather than a generic rejection.
	if err != nil {
		t.Fatalf("building: %v", err)
	}
}

func TestSpacesInTheMessageAreSentAsPercentTwentyNotPlus(t *testing.T) {
	// The difference between a code that arrives and one that does not.
	//
	// url.Values.Encode writes a space as "+". This gateway does not decode
	// that back to a space, so the operator compares "Dear+Customer,+your..."
	// against the registered DLT template, matches nothing, and drops the
	// message -- while the aggregator still answers SUBMIT_ACCEPTED with a
	// transaction id. Three codes were accepted and never delivered before
	// anybody looked at the encoding.
	var rawQuery string
	var decodedText string
	g := enterpriseGateway(t, func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		decodedText = r.URL.Query().Get("text")
		_, _ = io.WriteString(w, `{"transactionId":9,"state":"SUBMIT_ACCEPTED","statusCode":200}`)
	})

	const msg = "Dear Customer, your OTP is 456789. Do not share it."
	if _, err := g.Send(t.Context(), "report.acknowledged", "9876543210", msg); err != nil {
		t.Fatal(err)
	}

	if strings.Contains(rawQuery, "+") {
		t.Errorf("a bare + reached the query, so the provider will read it as "+
			"a literal plus rather than a space: %q", rawQuery)
	}
	if !strings.Contains(rawQuery, "%20") {
		t.Errorf("spaces were not encoded as %%20: %q", rawQuery)
	}
	// And a conforming reader still gets the original text back, so this is an
	// encoding choice rather than a corruption.
	if decodedText != msg {
		t.Errorf("the text decoded as %q, want %q", decodedText, msg)
	}
}
