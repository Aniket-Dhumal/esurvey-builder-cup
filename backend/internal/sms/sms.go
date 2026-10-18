// Package sms delivers text messages to farmers.
//
// # What was here before
//
// Nothing. The outbox, the Marathi templates, the opt-out handling and the
// dispatcher were all built and idle; the platform has never told a farmer
// anything. Its own notification package says this is "not a missing feature
// at the edge; it is most of the adoption problem", and it has been true
// since the first report was filed.
//
// # Why this is not just an HTTP POST
//
// India regulates commercial and transactional SMS through DLT. Every sender
// header is registered, every message template is registered, and an operator
// rejects anything that does not match a registered template for the header
// that sent it. A gateway that posts a well-formed message with no template
// id gets a 200 from the aggregator and delivers nothing, which is the worst
// possible failure: the platform believes it told somebody.
//
// So a message without a registered template id is refused here, before it
// reaches the wire, with an error naming the event that has no template. An
// operator reading that log can go and register one. An operator reading
// "delivered: 4,812" while farmers hear nothing cannot.
//
// # Why the provider is pluggable
//
// Whichever aggregator a state has procured is a procurement decision, not an
// engineering one, and it changes. The gateway holds the parts that are the
// same everywhere -- template enforcement, number normalisation, what counts
// as worth retrying -- and the transport holds the parts that are not.
package sms

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Transport is one aggregator's wire format.
type Transport interface {
	// Send delivers one message and returns the provider's reference.
	Send(ctx context.Context, m Outbound) (ref string, err error)
	// Name identifies the provider in the record.
	Name() string
}

// Outbound is one message, ready for the wire.
type Outbound struct {
	// To is a normalised Indian mobile number: ten digits, no country code,
	// no punctuation. Transports add whatever prefix they want.
	To string

	Body string

	// TemplateID is the DLT-registered template this message matches. An
	// operator rejects a message whose text does not correspond to a template
	// registered against the sender header, so this is not optional in India.
	TemplateID string

	// SenderID is the registered six-character header, e.g. ESURVY.
	SenderID string

	// Unicode says the body is not GSM-7. Every Marathi message is, and an
	// aggregator that is not told encodes it as Latin and delivers mojibake.
	Unicode bool
}

// Errors worth branching on.
var (
	// ErrNoTemplate is a message with no DLT template registered for it.
	// Permanent: retrying will not register a template.
	ErrNoTemplate = errors.New("no DLT template is registered for this message")

	// ErrBadNumber is a recipient that is not a mobile number this can reach.
	ErrBadNumber = errors.New("not a reachable Indian mobile number")

	// ErrRejected is the aggregator refusing the message. Permanent unless
	// the reason says otherwise.
	ErrRejected = errors.New("the gateway rejected the message")

	// ErrUnavailable is the aggregator being unreachable or failing. Worth
	// retrying; the outbox already knows how.
	ErrUnavailable = errors.New("the gateway is unavailable")
)

// Config is everything a deployment supplies.
type Config struct {
	Provider string
	BaseURL  string

	// Username is the account name, for gateways that authenticate with a
	// pair rather than a single key. AuthKey is that account's password in
	// that case, and the key itself in every other.
	Username string
	AuthKey  string
	SenderID string

	// PEID is the DLT Principal Entity ID the sender header is registered
	// under. Optional for most gateways and sent only when set.
	PEID string

	// Templates maps an event code to its DLT-registered template id.
	//
	// A map rather than a field per event so that registering a new template
	// is a configuration change, not a deployment.
	Templates map[string]string

	// DryRun logs what would be sent and reports success without sending.
	// For a rehearsal against real recipients, where the failure everyone
	// fears is a test run that actually messages four thousand farmers.
	DryRun bool

	Timeout time.Duration
}

// Gateway sends messages, having first refused the ones that cannot arrive.
type Gateway struct {
	transport Transport
	cfg       Config
}

// New builds a gateway for a configured provider.
func New(cfg Config, httpClient *http.Client) (*Gateway, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}
	if strings.TrimSpace(cfg.SenderID) == "" {
		return nil, errors.New(
			"no sender header is configured; an unregistered header is rejected " +
				"by every Indian operator")
	}

	var t Transport
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "msg91":
		t = &msg91{cfg: cfg, http: httpClient}
	case "enterprise", "fe":
		t = &enterprise{cfg: cfg, http: httpClient}
	case "generic", "http":
		t = &generic{cfg: cfg, http: httpClient}
	case "":
		return nil, errors.New("no SMS provider is configured")
	default:
		return nil, fmt.Errorf("unknown SMS provider %q", cfg.Provider)
	}

	return &Gateway{transport: t, cfg: cfg}, nil
}

// Name identifies the provider in the notification record.
func (g *Gateway) Name() string {
	if g.cfg.DryRun {
		return g.transport.Name() + " (dry run)"
	}
	return g.transport.Name()
}

// Send delivers one message for an event.
//
// eventCode selects the DLT template. A message whose event has no registered
// template is refused here rather than posted and silently dropped by an
// operator: a platform that believes it told somebody is worse off than one
// that knows it did not.
func (g *Gateway) Send(ctx context.Context, eventCode, to, body string) (string, error) {
	number, err := NormaliseIndianMobile(to)
	if err != nil {
		return "", err
	}

	template := g.cfg.Templates[eventCode]
	if strings.TrimSpace(template) == "" {
		return "", fmt.Errorf("%w: %s", ErrNoTemplate, eventCode)
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("refusing to send an empty message")
	}

	m := Outbound{
		To:         number,
		Body:       body,
		TemplateID: template,
		SenderID:   g.cfg.SenderID,
		Unicode:    NeedsUnicode(body),
	}

	if g.cfg.DryRun {
		// A reference that cannot be mistaken for a real one.
		return "dryrun-" + eventCode, nil
	}

	ctx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()

	return g.transport.Send(ctx, m)
}

// NormaliseIndianMobile reduces what people type to ten digits.
//
// Numbers arrive from a registry, from a farmer typing on a phone, and from a
// call centre. They arrive as +91 98765 43210, 09876543210, 0091-9876543210
// and 98765 43210, and they are all the same person. A gateway that treats
// them as four different numbers fails three of them.
func NormaliseIndianMobile(raw string) (string, error) {
	var digits strings.Builder
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	d := digits.String()

	switch {
	case len(d) == 10:
	case len(d) == 11 && d[0] == '0':
		d = d[1:]
	case len(d) == 12 && strings.HasPrefix(d, "91"):
		d = d[2:]
	case len(d) == 13 && strings.HasPrefix(d, "091"):
		d = d[3:]
	case len(d) == 14 && strings.HasPrefix(d, "0091"):
		d = d[4:]
	default:
		return "", fmt.Errorf("%w: %q has %d digits", ErrBadNumber, raw, len(d))
	}

	// Indian mobile numbers begin 6, 7, 8 or 9. A landline or a mistyped
	// number fails at the operator, and failing here says why.
	if d[0] < '6' {
		return "", fmt.Errorf(
			"%w: %s does not begin 6, 7, 8 or 9, so it is not a mobile", ErrBadNumber, d)
	}
	return d, nil
}

// NeedsUnicode reports whether a body needs UCS-2.
//
// Every Marathi message does. An aggregator not told encodes it as GSM-7 and
// the farmer receives question marks, which is indistinguishable at this end
// from a message that arrived.
func NeedsUnicode(body string) bool {
	for _, r := range body {
		if r > 0x7F {
			return true
		}
	}
	return false
}

// Segments is how many SMS a body will cost.
//
// Worth knowing before a district-wide send: a Marathi message is UCS-2, so
// it is 70 characters per segment and 67 once it is concatenated. Four
// hundred thousand farmers times an extra segment is a real number.
func Segments(body string) int {
	runes := len([]rune(body))
	if runes == 0 {
		return 0
	}
	if !NeedsUnicode(body) {
		if runes <= 160 {
			return 1
		}
		return (runes + 152) / 153
	}
	if runes <= 70 {
		return 1
	}
	return (runes + 66) / 67
}

// Retryable reports whether a failure is worth another attempt.
//
// The outbox holds the backoff; this only says which kind of failure it is.
// Retrying a rejected template burns the attempt budget on something no
// number of attempts will fix.
func Retryable(err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrUnavailable):
		return true
	case errors.Is(err, ErrNoTemplate),
		errors.Is(err, ErrBadNumber),
		errors.Is(err, ErrRejected):
		return false
	default:
		// An unclassified failure is more often a network than a refusal.
		return true
	}
}

// redact reduces a number to something safe to log.
//
// A log line is read by more people than a database row and is copied into
// tickets. The last four digits identify a number to somebody who already has
// it and to nobody else.
func redact(number string) string {
	if len(number) < 4 {
		return "****"
	}
	return "******" + number[len(number)-4:]
}
