package officeragent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agribid/esurvey-platform/backend/internal/platform/page"
	"github.com/agribid/esurvey-platform/backend/internal/rbac"
	"github.com/agribid/esurvey-platform/backend/internal/report"
)

// The agent's limits, checked rather than described.
//
// The prompt tells the model it cannot approve a claim. That is not what stops it:
// there being no tool that approves is what stops it. These tests are about the
// things that actually hold -- the turn cap, the scope every tool is given, what
// happens when a tool fails, and the fact that no write path exists at all.

// scriptedModel answers with whatever the test lines up.
type scriptedModel struct {
	replies []TurnResponse
	seen    []TurnRequest
	err     error
}

func (m *scriptedModel) Turn(_ context.Context, req TurnRequest) (*TurnResponse, error) {
	m.seen = append(m.seen, req)
	if m.err != nil {
		return nil, m.err
	}
	if len(m.replies) == 0 {
		return &TurnResponse{Text: "done"}, nil
	}
	next := m.replies[0]
	m.replies = m.replies[1:]
	return &next, nil
}

func officer() rbac.Scope {
	return rbac.Scope{JurisdictionIDs: []string{"11111111-1111-1111-1111-111111111111"}}
}

func TestAnAnswerWithNoToolIsReturnedAsIs(t *testing.T) {
	model := &scriptedModel{replies: []TurnResponse{{Text: "Four are waiting."}}}
	a := New(model, nil)

	got, err := a.Ask(context.Background(), officer(), "how many?", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "Four are waiting." {
		t.Fatalf("text %q", got.Text)
	}
	if got.Turns != 1 {
		t.Fatalf("turns %d, want 1", got.Turns)
	}
}

func TestEveryToolIsGivenTheOfficersOwnScope(t *testing.T) {
	// The whole basis of the design: what the agent can reach is what the officer
	// could reach by hand, enforced by the same code. A tool called with an empty
	// or substituted scope would quietly read the whole state.
	var saw rbac.Scope
	tool := Tool{
		Name:        "peek",
		Description: "looks",
		Run: func(_ context.Context, scope rbac.Scope, _ map[string]any) (any, error) {
			saw = scope
			return "ok", nil
		},
	}
	model := &scriptedModel{replies: []TurnResponse{
		{ToolCall: &ToolCall{Name: "peek"}},
		{Text: "read it"},
	}}

	want := officer()
	if _, err := New(model, []Tool{tool}).
		Ask(context.Background(), want, "look", nil, nil); err != nil {
		t.Fatal(err)
	}

	if len(saw.JurisdictionIDs) != 1 ||
		saw.JurisdictionIDs[0] != want.JurisdictionIDs[0] {
		t.Fatalf("tool saw scope %+v, want %+v", saw, want)
	}
}

func TestAnOfficerWithNoPostingIsToldSoRatherThanShownEverything(t *testing.T) {
	// An empty scope reaching a store would list nothing, which is correct but
	// looks identical to "there are no claims". Answered here instead.
	model := &scriptedModel{}
	got, err := New(model, nil).Ask(context.Background(), rbac.Scope{}, "how many?", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Text, "no jurisdiction") {
		t.Fatalf("text %q", got.Text)
	}
	if len(model.seen) != 0 {
		t.Fatal("asked the model about a posting that does not exist")
	}
}

func TestTheTurnBudgetIsEnforcedAndSaidPlainly(t *testing.T) {
	// A model that keeps calling tools must stop costing money, and the officer
	// must not be handed a half-finished chain as though it were an answer: it has
	// read some of the claims and none of the rest.
	calls := 0
	tool := Tool{
		Name:        "again",
		Description: "loops",
		Run: func(context.Context, rbac.Scope, map[string]any) (any, error) {
			calls++
			return "more", nil
		},
	}
	replies := make([]TurnResponse, 0, MaxTurns+2)
	for i := 0; i < MaxTurns+2; i++ {
		replies = append(replies, TurnResponse{ToolCall: &ToolCall{Name: "again"}})
	}
	model := &scriptedModel{replies: replies}

	got, err := New(model, []Tool{tool}).
		Ask(context.Background(), officer(), "go round", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != MaxTurns {
		t.Fatalf("ran %d tools, want %d", calls, MaxTurns)
	}
	if !strings.Contains(got.Text, "could not finish") {
		t.Fatalf("a truncated chain was presented as an answer: %q", got.Text)
	}
}

func TestAFailedToolBecomesSomethingTheModelKnows(t *testing.T) {
	// Not an exception the officer sees. The model may well answer another way,
	// and "the weather service was unreachable" beats a stack trace.
	tool := Tool{
		Name:        "weather",
		Description: "rain",
		Run: func(context.Context, rbac.Scope, map[string]any) (any, error) {
			return nil, errors.New("gauge unreachable")
		},
	}
	model := &scriptedModel{replies: []TurnResponse{
		{ToolCall: &ToolCall{Name: "weather"}},
		{Text: "I could not read the rainfall."},
	}}

	got, err := New(model, []Tool{tool}).
		Ask(context.Background(), officer(), "rain?", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "I could not read the rainfall." {
		t.Fatalf("text %q", got.Text)
	}
	if len(got.Steps) != 1 || got.Steps[0].Failed == "" {
		t.Fatalf("the failure was not recorded in the steps: %+v", got.Steps)
	}
	// And the model was actually told.
	last := model.seen[len(model.seen)-1]
	found := false
	for _, m := range last.Messages {
		if m.Role == "tool" && m.ToolName == "weather" {
			found = true
		}
	}
	if !found {
		t.Fatal("the model was not told the tool failed")
	}
}

func TestAToolThatDoesNotExistIsRefusedAndTheModelIsTold(t *testing.T) {
	// Silently dropping it would have the model ask again until the turn budget
	// went on a tool that was never there.
	model := &scriptedModel{replies: []TurnResponse{
		{ToolCall: &ToolCall{Name: "approve_claim"}},
		{Text: "I cannot do that."},
	}}
	got, err := New(model, nil).Ask(context.Background(), officer(), "approve it", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 1 || got.Steps[0].Failed != "no such tool" {
		t.Fatalf("steps %+v", got.Steps)
	}
	if got.Text != "I cannot do that." {
		t.Fatalf("text %q", got.Text)
	}
}

func TestNoToolCanWrite(t *testing.T) {
	// The claim the whole package rests on. Read against the tool set rather than
	// the prose: a write tool added later has to fail this test to get in.
	names := map[string]bool{}
	for _, tool := range ReadOnlyTools(stubReports{}) {
		names[tool.Name] = true
	}
	for _, forbidden := range []string{
		"approve_claim", "reject_claim", "return_claim", "reassign_claim",
		"close_claim", "set_damage_percent", "transition", "update_claim",
		"delete_claim",
	} {
		if names[forbidden] {
			t.Fatalf("a tool named %q exists; the approval ladder must stay "+
				"human-authored", forbidden)
		}
	}
	if len(names) == 0 {
		t.Fatal("no tools at all")
	}
}

func TestTheModelBeingUnreachableIsItsOwnFailure(t *testing.T) {
	// So the console can say the assistant is unavailable rather than implying the
	// question was improper. Nothing else on the platform depends on this working.
	model := &scriptedModel{err: errors.New("connection refused")}
	_, err := New(model, nil).Ask(context.Background(), officer(), "hello", nil, nil)
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("error %v, want ErrNoModel", err)
	}
}

func TestASuggestionInTheAgreedFormBecomesAProposal(t *testing.T) {
	model := &scriptedModel{replies: []TurnResponse{{
		Text: "This one is short of evidence. " +
			"[[suggest: return; report=CLR-2026-496-00001; reason=only one clip]]",
	}}}
	got, err := New(model, nil).Ask(context.Background(), officer(), "advise", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Proposed == nil {
		t.Fatal("no proposal")
	}
	if got.Proposed.Action != "return" {
		t.Fatalf("action %q", got.Proposed.Action)
	}
	if got.Proposed.ReportID != "CLR-2026-496-00001" {
		t.Fatalf("report %q", got.Proposed.ReportID)
	}
	if got.Proposed.Reason != "only one clip" {
		t.Fatalf("reason %q", got.Proposed.Reason)
	}
}

func TestProseThatMerelySoundsLikeAdviceDoesNotBecomeAButton(t *testing.T) {
	// The console offers a button for a proposal. A paragraph that reads like a
	// recommendation must stay a paragraph, or an officer ends up one tap from an
	// action nobody proposed in the agreed form.
	for _, text := range []string{
		"I would return this claim if I were you.",
		"You should probably approve it.",
		"suggest returning it",
		"[[suggest: obliterate; reason=no]]",
	} {
		if p := proposalFrom(text); p != nil {
			t.Fatalf("%q became a proposal: %+v", text, p)
		}
	}
}

func TestTheMarkerIsNotShownToTheOfficer(t *testing.T) {
	out := StripProposal(
		"Short of evidence. [[suggest: return; reason=one clip]] Worth a look.")
	if strings.Contains(out, "[[suggest") {
		t.Fatalf("the marker reached the screen: %q", out)
	}
	if !strings.Contains(out, "Short of evidence.") ||
		!strings.Contains(out, "Worth a look.") {
		t.Fatalf("the reply was damaged: %q", out)
	}
}

// stubReports satisfies Reports without a database.
type stubReports struct{}

func (stubReports) List(
	context.Context, report.Filter,
) (page.Result[report.Summary], error) {
	return page.Result[report.Summary]{}, nil
}

func (stubReports) Get(
	context.Context, string, rbac.Scope,
) (*report.Report, error) {
	return nil, errors.New("not found")
}

func (stubReports) ByReference(
	context.Context, string, rbac.Scope,
) (*report.Report, error) {
	return nil, errors.New("not found")
}
