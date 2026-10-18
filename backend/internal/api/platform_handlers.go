package api

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// What this platform is and is not connected to.
//
// # Why this is a screen and not a runbook
//
// Four capabilities are built end to end and delivering nothing: SMS to
// farmers, email, evidence in object storage, and threshold yields. Each is
// one credential away, and each degrades honestly rather than silently -- the
// queues hold what they could not send, and an assessment with no threshold
// yield says the percentage is not a shortfall against a statutory figure.
//
// Honest degradation is worth very little if nobody is looking. The backlog
// was visible only in /readyz, which is a JSON document meant for a load
// balancer, and the threshold coverage was not visible anywhere at all. So an
// operator's first sight of "no SMS has ever been delivered" was a farmer
// ringing to ask why nobody told them.
//
// This is that state as a page: what is connected, what is queued behind what
// is not, and the exact variable that turns each one on.
//
// # Why it never returns a credential
//
// Nothing here reads a secret. It reports whether one is present and what its
// variable is called, which is what an operator needs, and it does that from
// Configured()-style predicates rather than from the values. A status page
// that echoes an API key has published it to everyone who can read the page,
// its browser cache, and whatever logs the response.

type integration struct {
	Key   string `json:"key"`
	Name  string `json:"name"`
	State string `json:"state"` // live, degraded, unconfigured, failing
	// Summary is one line an operator can act on.
	Summary string `json:"summary"`
	// Consequence is what the platform is doing meanwhile. Empty when live.
	Consequence string `json:"consequence,omitempty"`
	// Enable names the variables that turn it on, never their values.
	Enable  []string `json:"enable,omitempty"`
	Metrics []metric `json:"metrics,omitempty"`
}

type metric struct {
	Label string `json:"label"`
	Value string `json:"value"`
	// Warn marks a number that is the problem rather than the context.
	Warn bool `json:"warn,omitempty"`
}

const (
	stateLive         = "live"
	stateDegraded     = "degraded"
	stateUnconfigured = "unconfigured"
	stateFailing      = "failing"
)

// integrations reports every external dependency and its live state.
func (s *Server) integrations(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	out := []integration{
		s.smsIntegration(ctx),
		s.emailIntegration(ctx),
		s.evidenceIntegration(ctx),
		s.thresholdIntegration(ctx),
		s.satelliteIntegration(),
		s.agentIntegration(ctx),
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"integrations": out,
		"environment":  s.Env,
	})
}

func (s *Server) smsIntegration(ctx context.Context) integration {
	in := integration{
		Key:  "sms",
		Name: "SMS to farmers",
		Enable: []string{
			"ESURVEY_SMS_PROVIDER", "ESURVEY_SMS_URL", "ESURVEY_SMS_AUTH_KEY",
			"ESURVEY_SMS_SENDER_ID", "ESURVEY_SMS_TEMPLATES",
		},
	}
	if s.Notify == nil {
		in.State = stateUnconfigured
		in.Summary = "No queue is running."
		in.Consequence = "Farmers are told nothing about their claims."
		return in
	}

	b, err := s.Notify.Backlog(ctx)
	if err != nil {
		in.State = stateFailing
		in.Summary = "The queue could not be read."
		return in
	}

	in.Metrics = []metric{
		{Label: "Waiting", Value: count(b.Queued), Warn: b.Queued > 0 && b.Provider == "none"},
		{Label: "Delivered", Value: count(b.Sent)},
		{Label: "Failed", Value: count(b.Failed), Warn: b.Failed > 0},
	}
	if b.Abandoned > 0 {
		in.Metrics = append(in.Metrics,
			metric{Label: "Given up on", Value: count(b.Abandoned), Warn: true})
	}

	switch {
	case b.Provider == "none":
		in.State = stateUnconfigured
		in.Summary = "No gateway is configured."
		in.Consequence = "Every message is being kept. Nothing is lost, and " +
			"nothing has reached a farmer."
		if age := waitingSince(b.Oldest); age != "" {
			in.Metrics = append(in.Metrics,
				metric{Label: "Oldest waiting since", Value: age, Warn: true})
		}
		// A DLT registration is the real blocker, not the code, and an
		// operator who does not know that will look for a bug.
		in.Consequence += " An Indian gateway needs a DLT-registered sender " +
			"ID and a template id per message before it will accept one."
	case b.Failed > 0:
		in.State = stateDegraded
		in.Summary = "Sending through " + b.Provider + ", with failures."
	default:
		in.State = stateLive
		in.Summary = "Sending through " + b.Provider + "."
	}
	return in
}

func (s *Server) emailIntegration(ctx context.Context) integration {
	in := integration{
		Key:  "email",
		Name: "Email",
		Enable: []string{
			"ESURVEY_SMTP_HOST", "ESURVEY_SMTP_PORT", "ESURVEY_SMTP_USERNAME",
			"ESURVEY_SMTP_PASSWORD", "ESURVEY_SMTP_FROM",
		},
	}
	if s.Mail == nil {
		in.State = stateUnconfigured
		in.Summary = "No mailer is running."
		return in
	}

	if n, err := s.Mail.Pending(ctx); err == nil {
		in.Metrics = []metric{{
			Label: "Waiting", Value: count(n),
			Warn: n > 0 && !s.Mail.Configured(),
		}}
	}

	if !s.Mail.Configured() {
		in.State = stateUnconfigured
		in.Summary = "No SMTP host is configured."
		// The workaround matters more than the fault here: an administrator
		// can still onboard officers, by handing over the link themselves.
		in.Consequence = "Invitations and password reset links are queued " +
			"rather than sent. An administrator can still copy an " +
			"invitation link from the officer accounts screen and pass it " +
			"on. A password reset cannot be worked around this way, by " +
			"design: only the holder of the mailbox may ask for one."
		return in
	}
	in.State = stateLive
	in.Summary = "Sending."
	return in
}

func (s *Server) evidenceIntegration(ctx context.Context) integration {
	in := integration{
		Key:  "evidence",
		Name: "Evidence storage",
		Enable: []string{
			"ESURVEY_EVIDENCE_BUCKET", "ESURVEY_EVIDENCE_PREFIX",
		},
	}
	if s.Captures == nil {
		in.State = stateUnconfigured
		in.Summary = "No evidence store is wired."
		return in
	}

	where := s.Captures.Evidence().Describe()
	in.Metrics = []metric{{Label: "Where", Value: where}}

	if err := s.Captures.Evidence().Check(ctx); err != nil {
		in.State = stateFailing
		in.Summary = "The evidence store is not answering."
		in.Consequence = "A survey cannot be accepted while a photograph " +
			"cannot be stored."
		return in
	}
	// A local directory is a working configuration and a fragile one, and
	// saying "live" without the caveat would let it survive to production.
	//
	// Asked through an optional interface, like the registry's Status() in
	// readyz, so a store that does not answer is not forced to. One that does
	// not is read as local, which is the cautious way round: a spurious
	// warning costs an operator a minute, and a missing one costs a season of
	// evidence.
	remote := false
	if r, ok := s.Captures.Evidence().(interface{ Remote() bool }); ok {
		remote = r.Remote()
	}
	if !remote {
		in.State = stateDegraded
		in.Summary = "Storing on the machine's own disk."
		in.Consequence = "Every photograph a claim rests on lives on one " +
			"server. Losing it loses the evidence for every open claim."
		return in
	}
	in.State = stateLive
	in.Summary = "Storing in object storage."
	return in
}

func (s *Server) thresholdIntegration(ctx context.Context) integration {
	in := integration{
		Key:    "thresholds",
		Name:   "Threshold yields",
		Enable: []string{"threshold_yields (loaded per crop, season and unit)"},
	}

	var issued int
	if err := s.DB.QueryRow(ctx,
		`SELECT count(*) FROM threshold_yields`).Scan(&issued); err != nil {
		in.State = stateFailing
		in.Summary = "The threshold table could not be read."
		return in
	}
	in.Metrics = []metric{{Label: "Figures on record", Value: count(issued), Warn: issued == 0}}

	if issued == 0 {
		in.State = stateUnconfigured
		in.Summary = "No statutory yield has been issued to this platform."
		// This is the one integration whose absence changes what a number
		// means rather than who hears about it.
		in.Consequence = "Assessments still produce a damage percentage, and " +
			"it is a loss of standing crop rather than a shortfall against " +
			"a threshold yield. Every assessment says so. A figure that is " +
			"to be read as a shortfall needs the district's own notified " +
			"yield per crop, per season, per unit."
		return in
	}
	in.State = stateLive
	in.Summary = "Loaded."
	return in
}

func (s *Server) satelliteIntegration() integration {
	in := integration{
		Key:    "satellite",
		Name:   "Analytics engine",
		Enable: []string{"ESURVEY_SATELLITE_URL", "SATELLITE_API_KEY"},
	}
	if s.Satellite == nil {
		in.State = stateUnconfigured
		in.Summary = "Not wired."
		in.Consequence = "New assessments cannot be made."
		return in
	}
	open, failures := s.Satellite.Health()
	switch {
	case open:
		in.State = stateFailing
		in.Summary = "Not answering."
		in.Consequence = "New assessments will fail until it answers again. " +
			"Past assessments and the whole console stay usable."
		in.Metrics = []metric{{Label: "Consecutive failures", Value: count(failures), Warn: true}}
	case failures > 0:
		in.State = stateDegraded
		in.Summary = "Answering, with recent failures."
		in.Metrics = []metric{{Label: "Consecutive failures", Value: count(failures), Warn: true}}
	default:
		in.State = stateLive
		in.Summary = "Answering."
	}
	return in
}

func (s *Server) agentIntegration(ctx context.Context) integration {
	in := integration{
		Key:    "agent",
		Name:   "Assessment panel",
		Enable: []string{"ESURVEY_AI_URL", "GEMINI_API_KEY"},
	}
	if s.AI == nil {
		in.State = stateUnconfigured
		in.Summary = "Not wired."
		in.Consequence = "New assessments cannot be made."
		return in
	}
	if _, err := s.AI.Health(ctx); err != nil {
		in.State = stateFailing
		in.Summary = "Not answering."
		in.Consequence = "New assessments will fail until it answers again."
		return in
	}
	in.State = stateLive
	in.Summary = "Answering."
	return in
}

// count formats a whole number with thousands separators, because a queue
// depth of 104829 and one of 10482 are indistinguishable at a glance.
func count(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	out := make([]byte, 0, len(s)+len(s)/3)
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// waitingSince turns the queue's timestamp into something an operator reads.
//
// The backlog reports an ISO instant in UTC, which is the right thing for a
// program and the wrong thing on a page: an operator in Solapur reading
// "2026-09-01T21:38:34Z" has to do arithmetic before they know whether that is
// last night or last week.
func waitingSince(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse("2006-01-02T15:04:05Z", iso)
	if err != nil {
		return iso
	}
	days := int(time.Since(t).Hours() / 24)
	when := t.In(ist).Format("2 January, 3:04 pm")
	switch {
	case days <= 0:
		return when
	case days == 1:
		return when + " (a day ago)"
	default:
		return fmt.Sprintf("%s (%d days ago)", when, days)
	}
}
