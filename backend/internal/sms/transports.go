package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// The two shapes an Indian aggregator comes in.
//
// msg91 is a concrete integration against a provider a great many Indian
// government and agri deployments already use. generic is a URL template for
// everyone else, because which aggregator a state has procured is a
// procurement decision that changes, and re-deploying the platform to follow
// it would be the wrong coupling.
//
// Both are deliberately thin. Everything that is the same everywhere --
// template enforcement, number normalisation, what is worth retrying, what is
// safe to log -- lives in the gateway above, so a new provider is a hundred
// lines and cannot quietly skip any of it.

// ---------------------------------------------------------------------------
// MSG91
// ---------------------------------------------------------------------------

type msg91 struct {
	cfg  Config
	http *http.Client
}

func (m *msg91) Name() string { return "msg91" }

// msg91Request is the v5 flow shape.
//
// The template id is the DLT template, and the body is passed as the single
// variable that template declares. A template with a different variable
// layout needs its own mapping; that is a registration-time decision and
// belongs in configuration, not here.
type msg91Request struct {
	TemplateID string `json:"template_id"`
	ShortURL   string `json:"short_url,omitempty"`
	Recipients []struct {
		Mobiles string `json:"mobiles"`
		Body    string `json:"body"`
	} `json:"recipients"`
}

type msg91Response struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	// RequestID is the provider's reference for a delivery report.
	RequestID string `json:"request_id"`
}

func (m *msg91) Send(ctx context.Context, out Outbound) (string, error) {
	base := m.cfg.BaseURL
	if base == "" {
		base = "https://control.msg91.com/api/v5/flow/"
	}

	req := msg91Request{TemplateID: out.TemplateID}
	req.Recipients = append(req.Recipients, struct {
		Mobiles string `json:"mobiles"`
		Body    string `json:"body"`
	}{
		// Country code required on the wire even though the platform holds
		// ten digits: the same number reaches a different person in a
		// different country, and guessing is not an option.
		Mobiles: "91" + out.To,
		Body:    out.Body,
	})

	buf, err := json.Marshal(req)
	if err != nil {
		return "", err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, base,
		bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("accept", "application/json")
	httpReq.Header.Set("authkey", m.cfg.AuthKey)

	resp, err := m.http.Do(httpReq)
	if err != nil {
		// A network failure is the gateway being unavailable, which the
		// outbox should retry. A rejection is not.
		return "", fmt.Errorf("%w: reaching msg91: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var out91 msg91Response
	_ = json.Unmarshal(raw, &out91)

	switch {
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: msg91 returned http %d", ErrUnavailable, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", fmt.Errorf("%w: msg91 is rate limiting", ErrUnavailable)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: msg91 returned http %d: %s",
			ErrRejected, resp.StatusCode, firstLine(out91.Message, raw))
	case strings.EqualFold(out91.Type, "error"):
		// A 200 with an error body. Common, and the reason a naive
		// integration reports every message as delivered.
		return "", fmt.Errorf("%w: %s", ErrRejected, firstLine(out91.Message, raw))
	}

	if out91.RequestID == "" {
		// Accepted with no reference. Not a failure -- the message may well
		// arrive -- but nothing can be traced to it afterwards.
		return "accepted-no-reference", nil
	}
	return out91.RequestID, nil
}

// ---------------------------------------------------------------------------
// Enterprise HTTP gateway (the "fe/api/v1" family)
// ---------------------------------------------------------------------------

// enterprise is the aggregator whose API is documented as "HTTP SMS API",
// serving /fe/api/v1/send (GET) and /fe/api/v1/message (JSON POST).
//
// # Why not the generic transport
//
// It nearly fits, and that is the danger. The generic transport reads success
// from the absence of the words "error", "invalid", "fail" and "reject" in the
// body, and this provider answers a refusal with a JSON status code and a
// description that often contains none of them:
//
//	{"statusCode":6001,"description":"Insufficient Balance!"}
//	{"statusCode":7001,"description":"DLT_Content_ID not found"}
//	{"statusCode":2051,"description":"Sender [ESURVY] doesn't exist"}
//
// Every one of those would have been read as sent. A farmer waiting for a code
// that a zero balance swallowed, and an operator reading a delivery count that
// says it went, is the exact failure this package was written to refuse.
//
// # Why the GET endpoint and not the JSON one
//
// The JSON endpoint (/fe/api/v1/message) was used first, on the reasoning that
// the documented GET carries the account password as a query parameter and a
// header is the better place for a credential. That reasoning is sound and the
// endpoint does not deliver: a message posted to it came back
// SUBMIT_ACCEPTED with a transaction id, and never reached the handset. The
// same message sent as a GET to /fe/api/v1/send arrives.
//
// Delivery is not a preference. The credential travels inside a TLS request
// rather than in the clear, so it is not exposed to the network; what it does
// reach is the provider's own access log, which is a real cost and the reason
// this comment exists. Rotate the gateway password on a schedule.
type enterprise struct {
	cfg  Config
	http *http.Client
}

func (e *enterprise) Name() string { return "sms-http-enterprise" }

// enterpriseRequest is the JSON body the /message endpoint takes.
//
// Unused by Send, which uses the GET form that actually delivers. Kept because
// it documents the other endpoint's shape, and because the day the provider
// fixes /message this is what to go back to.
type enterpriseRequest struct {
	Extra   map[string]string `json:"extra"`
	Message struct {
		Recipient string `json:"recipient"`
		Text      string `json:"text"`
	} `json:"message"`
	Sender  string `json:"sender"`
	Unicode bool   `json:"unicode"`
}

// enterpriseResponse covers both the acceptance and the refusal, which share
// a shape: the refusal simply carries a statusCode that is not 200 and no
// transaction id.
type enterpriseResponse struct {
	TransactionID json.Number `json:"transactionId"`
	State         string      `json:"state"`
	StatusCode    int         `json:"statusCode"`
	Description   string      `json:"description"`
	PDU           int         `json:"pdu"`
}

func (e *enterprise) Send(ctx context.Context, out Outbound) (string, error) {
	base := strings.TrimSpace(e.cfg.BaseURL)
	if base == "" {
		return "", fmt.Errorf("%w: no gateway URL is configured", ErrRejected)
	}
	if e.cfg.Username == "" || e.cfg.AuthKey == "" {
		return "", fmt.Errorf(
			"%w: the gateway needs a username and a password", ErrRejected)
	}

	q := url.Values{}
	q.Set("username", e.cfg.Username)
	q.Set("password", e.cfg.AuthKey)
	q.Set("from", out.SenderID)
	// Country code on the wire: the same ten digits reach a different person
	// in a different country, and the documented examples carry 91.
	q.Set("to", "91"+out.To)
	q.Set("text", out.Body)
	q.Set("unicode", strconv.FormatBool(out.Unicode))
	q.Set("dltContentId", out.TemplateID)
	if e.cfg.PEID != "" {
		q.Set("dltPeid", e.cfg.PEID)
	}

	// Spaces as %20, not +.
	//
	// url.Values.Encode writes a space as "+", which is correct for
	// application/x-www-form-urlencoded and is what every conforming decoder
	// reads back as a space. This gateway does not: a message sent that way is
	// accepted, given a transaction id, and never delivered, because the text
	// the operator compares against the registered DLT template arrives as
	// "Dear+Customer,+your..." and matches nothing.
	//
	// curl --data-urlencode writes %20, which is why the same message sent by
	// hand arrived and the same message sent from here did not.
	//
	// Safe as a blunt replacement: Encode has already percent-escaped any
	// literal plus in a value as %2B, so every bare + left in the string is a
	// space this is putting back.
	query := strings.ReplaceAll(q.Encode(), "+", "%20")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"?"+query, nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := e.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("%w: reaching the sms gateway: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var answer enterpriseResponse
	_ = json.Unmarshal(raw, &answer)

	switch {
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: the gateway returned http %d",
			ErrUnavailable, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", fmt.Errorf("%w: the gateway is rate limiting", ErrUnavailable)
	case resp.StatusCode == http.StatusUnauthorized:
		// Wrong credentials are not worth retrying, and saying so plainly
		// saves an operator reading a delivery report that blames the network.
		return "", fmt.Errorf("%w: the gateway refused the credentials", ErrRejected)
	case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted:
		return "", fmt.Errorf("%w: the gateway returned http %d: %s",
			ErrRejected, resp.StatusCode, firstLine(answer.Description, raw))
	}

	// A 200 that is not an acceptance. This is the case the generic transport
	// cannot see, and the reason this transport exists.
	if answer.StatusCode != 0 && answer.StatusCode != http.StatusOK {
		err := fmt.Errorf("%w: %s", ErrRejected,
			firstLine(fmt.Sprintf("status %d: %s", answer.StatusCode, answer.Description), raw))
		// A zero balance is the one refusal that fixes itself: the outbox
		// should hold the message rather than mark it undeliverable, because
		// somebody is about to top the account up.
		if answer.StatusCode == enterpriseNoBalance {
			return "", fmt.Errorf("%w: the gateway account is out of credit", ErrUnavailable)
		}
		return "", err
	}
	if answer.State != "" && !strings.EqualFold(answer.State, "SUBMIT_ACCEPTED") {
		return "", fmt.Errorf("%w: %s", ErrRejected,
			firstLine(answer.State+": "+answer.Description, raw))
	}

	if answer.TransactionID.String() == "" {
		return "accepted-no-reference", nil
	}
	return answer.TransactionID.String(), nil
}

// enterpriseNoBalance is the documented code for zero credit.
const enterpriseNoBalance = 6001

// ---------------------------------------------------------------------------
// Generic
// ---------------------------------------------------------------------------

// generic posts form values to a URL, substituting placeholders.
//
// Placeholders in BaseURL and in the body template:
//
//	{to}        the ten-digit number
//	{body}      the message, URL-encoded
//	{sender}    the registered header
//	{template}  the DLT template id
//	{key}       the auth key
//	{unicode}   "1" when the body needs UCS-2, "0" otherwise
//
// Enough for every aggregator seen in Indian government tenders, and a state
// that procures a different one changes an environment variable.
type generic struct {
	cfg  Config
	http *http.Client
}

func (g *generic) Name() string { return "sms-http" }

func (g *generic) Send(ctx context.Context, out Outbound) (string, error) {
	if g.cfg.BaseURL == "" {
		return "", fmt.Errorf("%w: no gateway URL is configured", ErrRejected)
	}

	target := substitute(g.cfg.BaseURL, out, g.cfg.AuthKey, url.QueryEscape)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", err
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: reaching the sms gateway: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	body := strings.TrimSpace(string(raw))

	switch {
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("%w: the gateway returned http %d",
			ErrUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: the gateway returned http %d: %s",
			ErrRejected, resp.StatusCode, firstLine("", raw))
	}

	// Many Indian aggregators answer 200 with a plain-text body that says
	// whether it worked. There is no standard, so the honest reading is to
	// look for a refusal and treat everything else as accepted -- and to keep
	// the body as the reference so an operator can trace it either way.
	lower := strings.ToLower(body)
	for _, bad := range []string{"error", "invalid", "fail", "reject", "unauthor"} {
		if strings.Contains(lower, bad) {
			return "", fmt.Errorf("%w: %s", ErrRejected, firstLine("", raw))
		}
	}

	if body == "" {
		return "accepted-no-reference", nil
	}
	return firstLine("", raw), nil
}

// substitute fills the placeholders, escaping with the caller's function.
func substitute(tmpl string, out Outbound, key string, esc func(string) string) string {
	unicode := "0"
	if out.Unicode {
		unicode = "1"
	}
	return strings.NewReplacer(
		"{to}", esc(out.To),
		"{body}", esc(out.Body),
		"{sender}", esc(out.SenderID),
		"{template}", esc(out.TemplateID),
		"{key}", esc(key),
		"{unicode}", unicode,
	).Replace(tmpl)
}

// firstLine is a short, safe excerpt of a provider's answer.
//
// Bounded because an aggregator that returns an HTML error page would
// otherwise put a kilobyte of markup into an error a person reads, and into
// the last_error column of every failed notification.
func firstLine(preferred string, raw []byte) string {
	s := strings.TrimSpace(preferred)
	if s == "" {
		s = strings.TrimSpace(string(raw))
	}
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "no message"
	}
	return s
}
