package audit

import (
	"context"
	"testing"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// The audit trail is what answers a grievance years later, and it is
// append-only at the database — so anything lost here is lost permanently and
// silently. What matters is that an entry cannot be quietly suppressed by
// malformed input, and that an entry always says who and when.

func TestAnEntryAlwaysHasATimeAndAnOutcome(t *testing.T) {
	// A row with no timestamp cannot be placed in a sequence of events, and a
	// row with no outcome cannot answer "was this allowed". Both have to be
	// filled in rather than left to the caller to remember.
	got := normalise(Event{Action: "report.read"})

	if got.At.IsZero() {
		t.Error("an entry with no timestamp was accepted")
	}
	if got.Outcome == "" {
		t.Error("an entry with no outcome was accepted")
	}
	if got.Outcome != OutcomeAllow {
		t.Errorf("default outcome = %q, want %q", got.Outcome, OutcomeAllow)
	}
	if got.Detail == nil {
		// A nil map marshals to JSON null, and the column is NOT NULL with a
		// '{}' default — a nil here is an insert that fails and an entry
		// that never lands.
		t.Error("Detail is nil; the insert would be rejected")
	}
}

func TestAnExplicitTimeIsNotOverwritten(t *testing.T) {
	// Backfilling an event that happened earlier must keep its own time, or
	// the trail reorders itself.
	when := time.Date(2025, 10, 12, 14, 0, 0, 0, time.UTC)
	if got := normalise(Event{At: when}); !got.At.Equal(when) {
		t.Errorf("At = %v, want %v", got.At, when)
	}
}

func TestAnExplicitOutcomeIsNotOverwritten(t *testing.T) {
	// The default is allow. A denial silently becoming an allow would invert
	// the one fact the entry exists to record.
	if got := normalise(Event{Outcome: OutcomeDeny}); got.Outcome != OutcomeDeny {
		t.Errorf("Outcome = %q, want %q", got.Outcome, OutcomeDeny)
	}
}

func TestAMalformedAddressCannotSuppressAnEntry(t *testing.T) {
	// A forwarded header is attacker-influenced. If a bad value made the
	// insert fail, anyone could stop themselves being logged by sending a
	// malformed X-Forwarded-For.
	for _, bad := range []string{
		"not-an-address",
		"999.999.999.999",
		"<script>",
		"127.0.0.1, 10.0.0.1", // a list, not an address
		"",
	} {
		if got := nullIP(bad); got != nil {
			t.Errorf("nullIP(%q) = %v, want nil", bad, got)
		}
	}

	for _, good := range []string{"127.0.0.1", "10.4.2.9", "::1", "2001:db8::1"} {
		if got := nullIP(good); got == nil {
			t.Errorf("nullIP(%q) dropped a valid address", good)
		}
	}
}

func TestEmptyStringsBecomeNullNotEmpty(t *testing.T) {
	// The difference between "no resource was named" and "a resource named
	// empty string" matters when querying the trail later.
	if nullString("") != nil {
		t.Error(`nullString("") should be nil`)
	}
	if nullString("report") == nil {
		t.Error("nullString dropped a real value")
	}
}

func TestFromContextFillsInTheActor(t *testing.T) {
	// An entry that does not say who did it answers nothing.
	p := &rbac.Principal{
		UserID: "u-1",
		Name:   "S. Pawar",
		Grants: []rbac.Grant{{Role: rbac.RoleTalathi}},
	}
	ctx := rbac.WithPrincipal(context.Background(), p)

	got := Event{Action: "report.read"}.FromContext(ctx)
	if got.ActorUserID != "u-1" {
		t.Errorf("ActorUserID = %q", got.ActorUserID)
	}
	if got.ActorLabel != "S. Pawar" {
		t.Errorf("ActorLabel = %q", got.ActorLabel)
	}
	if got.ActorRole == "" {
		t.Error("ActorRole is empty")
	}
}

func TestAnExplicitLabelSurvivesTheContext(t *testing.T) {
	// A grievance filed at a counter records the farmer as the subject while
	// an officer is the authenticated principal. The caller's label wins.
	p := &rbac.Principal{UserID: "u-1", Name: "S. Pawar"}
	ctx := rbac.WithPrincipal(context.Background(), p)

	got := Event{ActorLabel: "filed for H. Dhumal"}.FromContext(ctx)
	if got.ActorLabel != "filed for H. Dhumal" {
		t.Errorf("ActorLabel = %q; the caller's label was overwritten", got.ActorLabel)
	}
	if got.ActorUserID != "u-1" {
		t.Error("the authenticated actor was lost")
	}
}

func TestAnUnauthenticatedContextIsNotAnError(t *testing.T) {
	// A failed sign-in has no principal and is exactly the entry an inquiry
	// wants. It must record, with no actor.
	got := Event{Action: "auth.login", Outcome: OutcomeDeny}.
		FromContext(context.Background())

	if got.ActorUserID != "" {
		t.Errorf("ActorUserID = %q, want empty", got.ActorUserID)
	}
	if got.Action != "auth.login" || got.Outcome != OutcomeDeny {
		t.Error("the event was altered by an empty context")
	}
}
