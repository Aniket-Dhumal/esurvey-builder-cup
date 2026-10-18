package officeragent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Message is one turn already spoken, or one tool result already obtained.
type Message struct {
	Role       string `json:"role"`
	Text       string `json:"text,omitempty"`
	ToolName   string `json:"tool_name,omitempty"`
	ToolResult any    `json:"tool_result,omitempty"`

	// Attachments are the files the officer attached to this turn.
	//
	// They belong to the message rather than to the request because that is
	// where they belong in the conversation: a file is part of the question it
	// came with, and the model needs it in that position to answer "what does
	// this circular say about late filing". It follows that the files are resent
	// on every turn of the loop, which is what the bounds in attachment.go are
	// sized against.
	//
	// The console does not send these back in the history of a later question.
	// An attachment is an upload, not a transcript, and asking the officer's
	// browser to re-upload a scan on every follow-up would cost them their line
	// to say nothing new. What the model said about the file is in the history;
	// if the next question needs the file again, it is attached again.
	Attachments []Attachment `json:"attachments,omitempty"`
}

// ToolSpec describes a tool to the model.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolCall is one tool the model wants run.
type ToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// TurnRequest is one round trip to the AI service.
type TurnRequest struct {
	SystemInstruction string     `json:"system_instruction"`
	Messages          []Message  `json:"messages"`
	Tools             []ToolSpec `json:"tools,omitempty"`
	Model             string     `json:"model,omitempty"`
}

// TurnResponse is what to say, or what to run.
type TurnResponse struct {
	Text      string    `json:"text"`
	ToolCall  *ToolCall `json:"tool_call"`
	Model     string    `json:"model"`
	LatencyMS int       `json:"latency_ms"`
}

// HTTPTurner reaches the AI service.
//
// A separate client from internal/ai rather than a method on it: that one carries
// the assessment contract, which is large, versioned and shared with the panel. A
// conversation turn has nothing to do with an assessment, and folding the two
// together would mean every change to one recompiled the other.
type HTTPTurner struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewHTTPTurner builds a turner against the AI service.
//
// The timeout is generous but finite. An officer is waiting, and a model call that
// has not answered in a minute is not going to.
func NewHTTPTurner(baseURL, token string) *HTTPTurner {
	return &HTTPTurner{
		BaseURL: baseURL,
		Token:   token,
		Client:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Turn asks the model what to do next.
func (t *HTTPTurner) Turn(ctx context.Context, req TurnRequest) (*TurnResponse, error) {
	if t == nil || t.BaseURL == "" {
		return nil, ErrNoModel
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("encoding the turn: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.BaseURL+"/api/v1/agent/turn", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building the turn request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if t.Token != "" {
		// Bearer, which is what the AI service's require_caller reads, and what
		// internal/ai/client.go has always sent.
		//
		// This was X-Esurvey-Caller, a header nothing checks: the service accepts
		// Authorization or X-API-Key and treats anything else as no credential at
		// all. Every question would have come back 401, surfaced to the officer as
		// "the assistant could not be reached" -- a failure that looks like the model
		// being down and is a header name.
		//
		// Found by comparing the two ends rather than by a test, because both ends
		// were tested against their own idea of the contract and neither against the
		// other.
		httpReq.Header.Set("Authorization", "Bearer "+t.Token)
	}

	res, err := t.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("reaching the assistant: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		// The body is not quoted back. It can carry the prompt, and the prompt can
		// carry the claim, and an error string travels into logs and screens that
		// the claim itself does not.
		return nil, fmt.Errorf("the assistant answered %d", res.StatusCode)
	}

	var out TurnResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("reading the assistant's answer: %w", err)
	}
	return &out, nil
}
