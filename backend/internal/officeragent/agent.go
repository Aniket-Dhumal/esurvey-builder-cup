// Package officeragent answers an officer's questions about their own claims.
//
// # What it may do
//
// Read, and draft. It can look up claims, count them, summarise them and fill a
// report, and it can propose an action for the officer to confirm. It cannot
// approve, return, reassign, change a figure or close anything.
//
// That is not a limit imposed for caution's sake. The approval ladder and the
// audit trail are what answer an appeal -- "who decided this, and when" -- and
// they are only worth anything if a person authored them. An agent that approves
// claims produces an audit record saying an officer approved them, which would be
// false in the way that matters. So a proposed action leaves here as a structured
// suggestion and is executed, if at all, by the officer pressing the same button
// they press today, through the same route, with the same record.
//
// It follows that this package performs no writes at all. There is no draft table
// and nothing to clean up: a proposal that the officer ignores simply ends with the
// conversation.
//
// # Why the loop is here and the model is not
//
// Because the tools are here. Every tool is a call into the stores this service
// already owns, made with the officer's own rbac.Scope -- so what the agent can
// reach is exactly what that officer could reach by hand, enforced by the same
// code, with no second copy of the rules to drift. The model runs in the AI
// service, which holds the credential and applies the data boundary; it is asked
// what to do next and answers with one tool to call.
//
// A tool it asks for is a request. This decides whether to run it.
package officeragent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/agribid/esurvey-platform/backend/internal/rbac"
)

// MaxTurns bounds one question.
//
// Six is enough for a real chain -- find the claims, read one, read its
// assessment, read the weather behind it, answer -- and short enough that a model
// which has started going in circles stops costing money and the officer gets told
// rather than left watching a spinner.
const MaxTurns = 6

// ErrNoModel is returned when the AI service is not configured or not reachable.
//
// Distinguished from a refusal so the console can say "the assistant is
// unavailable" rather than implying the question was improper. Every other feature
// of the platform keeps working: nothing here is on the path of filing or assessing
// a claim.
var ErrNoModel = errors.New("the assistant is not available")

// Turner is one round trip to the model. Satisfied by the AI service client, and
// by a stand-in in tests.
type Turner interface {
	Turn(ctx context.Context, req TurnRequest) (*TurnResponse, error)
}

// Tool is one thing the agent may do.
//
// Run receives the arguments the model proposed and the scope of the officer who
// asked. It returns whatever the model should see -- a struct, a slice, a count --
// which the AI service redacts on the way past.
type Tool struct {
	Name        string
	Description string

	// Parameters is JSON Schema, in the subset the model's function declarations
	// accept. Nil for a tool that takes none.
	Parameters map[string]any

	Run func(ctx context.Context, scope rbac.Scope, args map[string]any) (any, error)
}

// Agent answers questions within one officer's posting.
type Agent struct {
	model Turner
	tools map[string]Tool
	log   *slog.Logger
}

// New builds an agent over a set of tools.
func New(model Turner, tools []Tool) *Agent {
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
	}
	return &Agent{model: model, tools: byName, log: slog.Default()}
}

// WithLogger replaces the logger.
func (a *Agent) WithLogger(l *slog.Logger) *Agent {
	a.log = l
	return a
}

// WithTools offers the agent one more set of tools, for the span of one question.
//
// A copy rather than a mutation, because what a question may do depends on who
// asked it and what they attached: the agent built at startup is shared by every
// request in the process and must not acquire one officer's attached workbook.
// Nothing here is a second agent -- the same model, the same loop, the same
// logger -- so the only thing that differs between two questions is what each
// was allowed to do.
func (a *Agent) WithTools(extra ...Tool) *Agent {
	if len(extra) == 0 {
		return a
	}
	tools := make(map[string]Tool, len(a.tools)+len(extra))
	for name, t := range a.tools {
		tools[name] = t
	}
	for _, t := range extra {
		tools[t.Name] = t
	}
	return &Agent{model: a.model, tools: tools, log: a.log}
}

// Answer is what the officer is shown.
type Answer struct {
	// Text is the reply, in the officer's own words where the model managed one.
	Text string `json:"text"`

	// Steps is what was actually done to answer, named tool by tool.
	//
	// Shown rather than hidden. An officer acting on a figure needs to know where
	// it came from, and "I read these four claims" is the difference between an
	// answer they can check and one they must take on trust.
	Steps []Step `json:"steps"`

	// Proposed is an action the officer may confirm, where the model suggested
	// one. Never performed here.
	Proposed *Proposal `json:"proposed,omitempty"`

	// PreparedExports are the documents this question produced, in the order
	// they were prepared.
	//
	// The answer carries the file because the question asked for it: "give me
	// the pending Barshi claims as a PDF" is one sentence, and an officer should
	// not have to read a paragraph about their claims and then go and press a
	// button hoping it means the same thing. The console renders one download
	// per entry here.
	//
	// What is in each entry is what a button needs -- a name, a format, a count,
	// a size -- and an id for the download route. The bytes are not here and
	// never reach the model: see exporttools.go.
	PreparedExports []Prepared `json:"prepared_exports,omitempty"`

	Turns int `json:"turns"`
}

// Step is one tool that ran.
type Step struct {
	Tool     string `json:"tool"`
	Summary  string `json:"summary"`
	Failed   string `json:"failed,omitempty"`
	Duration int64  `json:"duration_ms"`
}

// Proposal is an action put to the officer.
//
// Carried as a description and a route, not as a prepared write: the console
// renders it beside a button that calls the ordinary endpoint, so the action is
// taken by the officer through the path that records who took it.
type Proposal struct {
	Action   string `json:"action"`
	ReportID string `json:"report_id,omitempty"`
	Reason   string `json:"reason"`
}

// Ask answers one question, together with whatever the officer attached to it.
//
// The attachments ride on the officer's own message, which is where a file belongs
// in a conversation: "what does this circular say about late filing" is one
// question and one attachment, not a question and a separate upload. The AI service
// turns each into the part the model reads it as. See attachment.go for what may be
// attached, and for why nothing in a file changes what this is allowed to do.
func (a *Agent) Ask(
	ctx context.Context, scope rbac.Scope, question string, history []Message,
	attached []Attachment,
) (*Answer, error) {
	if a.model == nil {
		return nil, ErrNoModel
	}
	if scope.Empty() {
		// No posting, nothing to answer about. Not an error the officer caused.
		return &Answer{Text: "You have no jurisdiction assigned, so there is " +
			"nothing for me to look at."}, nil
	}

	messages := append([]Message{}, history...)
	messages = append(messages, Message{
		Role: "user", Text: question, Attachments: attached,
	})

	answer := &Answer{Steps: []Step{}}

	for turn := 0; turn < MaxTurns; turn++ {
		answer.Turns = turn + 1

		res, err := a.model.Turn(ctx, TurnRequest{
			SystemInstruction: instruction(scope, a.tools),
			Messages:          messages,
			Tools:             a.specs(),
		})
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoModel, err)
		}

		if res.ToolCall == nil {
			answer.Text = strings.TrimSpace(res.Text)
			if answer.Text == "" {
				answer.Text = "I could not work out an answer to that."
			}
			answer.Proposed = proposalFrom(res.Text)
			return answer, nil
		}

		call := res.ToolCall
		tool, known := a.tools[call.Name]
		if !known {
			// The model asked for something that does not exist. Told, rather
			// than silently dropped: it will otherwise ask again, and the turn
			// budget goes on a tool that was never there.
			a.log.Warn("agent asked for an unknown tool", "tool", call.Name)
			messages = append(messages, Message{
				Role:       "tool",
				ToolName:   call.Name,
				ToolResult: map[string]any{"error": "no such tool"},
			})
			answer.Steps = append(answer.Steps, Step{
				Tool: call.Name, Failed: "no such tool",
			})
			continue
		}

		started := time.Now()
		out, err := tool.Run(ctx, scope, call.Arguments)
		took := time.Since(started).Milliseconds()

		step := Step{Tool: call.Name, Duration: took}
		if err != nil {
			// A failed tool is a fact the model should have, not an exception
			// the officer should see. It may well be able to answer another way,
			// and "the weather service was unreachable" is a better reply than a
			// stack trace.
			a.log.Warn("agent tool failed", "tool", call.Name, "error", err)
			step.Failed = err.Error()
			answer.Steps = append(answer.Steps, step)
			messages = append(messages, Message{
				Role:       "tool",
				ToolName:   call.Name,
				ToolResult: map[string]any{"error": err.Error()},
			})
			continue
		}

		// A tool that prepared a document hands back two things at once: a
		// sentence for the model and a document for the officer. They part
		// company here, which is what keeps an id and a set of bytes out of the
		// conversation entirely -- the model is told what was prepared and is
		// given no way to refer to it.
		if prepared, ok := out.(preparedResult); ok {
			answer.PreparedExports = append(
				answer.PreparedExports, prepared.prepared)
			out = prepared.summary
		}

		step.Summary = summarise(out)
		answer.Steps = append(answer.Steps, step)
		messages = append(messages, Message{
			Role:       "tool",
			ToolName:   call.Name,
			ToolResult: out,
		})
	}

	// Out of turns. Said plainly rather than presented as an answer: a partial
	// chain that stopped early has read some of the claims and none of the rest,
	// and an officer must not mistake that for a complete picture.
	answer.Text = "I could not finish working that out within the steps I am " +
		"allowed. Try asking about a narrower set of claims."
	return answer, nil
}

func (a *Agent) specs() []ToolSpec {
	out := make([]ToolSpec, 0, len(a.tools))
	for _, t := range a.tools {
		out = append(out, ToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		})
	}
	return out
}

// summarise describes a tool result for the step list, without reproducing it.
//
// The officer sees what was consulted, not the payload: the answer above is where
// the content belongs, and a step list that printed every claim would bury it.
func summarise(out any) string {
	switch v := out.(type) {
	case nil:
		return "nothing"
	case string:
		if len(v) > 80 {
			return v[:80] + "…"
		}
		return v
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return "a result"
	}
	if len(raw) > 120 {
		return fmt.Sprintf("%d bytes", len(raw))
	}
	return string(raw)
}
