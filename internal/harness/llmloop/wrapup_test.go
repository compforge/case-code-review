package llmloop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// scriptedClient replays canned tool-call responses and records every request
// it receives, so tests can assert on injected wrap-up messages.
type scriptedClient struct {
	requests  []llm.ChatRequest
	responses []*llm.ChatResponse
}

func (c *scriptedClient) CompletionsWithCtx(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.requests = append(c.requests, req)
	i := len(c.requests) - 1
	if i >= len(c.responses) {
		i = len(c.responses) - 1
	}
	return c.responses[i], nil
}

func toolCallResp(name, args string) *llm.ChatResponse {
	return &llm.ChatResponse{Choices: []llm.Choice{{Message: llm.ResponseMessage{
		Role:      "assistant",
		ToolCalls: []llm.ToolCall{{ID: "c1", Type: "function", Function: llm.FunctionCall{Name: name, Arguments: args}}},
	}}}}
}

// echoProvider is a registered dummy tool that always returns data, keeping
// the loop in its normal (non-empty-round) path.
type echoProvider struct{}

func (echoProvider) Tool() tool.Tool { return tool.CodeSearch }
func (echoProvider) Execute(context.Context, map[string]any) (string, error) {
	return "some result", nil
}

func newWrapUpRunner(client *scriptedClient, maxRounds int) *Runner {
	reg := tool.NewRegistry()
	reg.Register(echoProvider{})
	reg.Freeze()
	return NewRunner(Deps{
		LLMClient: client,
		Template:  template.Template{MaxToolRequestTimes: maxRounds, MaxTokens: 100},
		Tools:     reg,
		Session:   session.New(".", "test", "m", session.SessionOptions{}),
	})
}

func lastUserMessage(req llm.ChatRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			if s, ok := req.Messages[i].Content.(string); ok {
				return s
			}
			return ""
		}
	}
	return ""
}

func scope() session.Scope {
	return session.Scope{ID: "a.go", Kind: "file", Type: "file", Paths: []string{"a.go"}}
}

func TestWrapUp_DeadlineForcesVerdictTurn(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResp("task_done", `{}`),
	}}
	r := newWrapUpRunner(client, 10)

	// Deadline well inside wrapUpTimeReserve → wrap-up must be injected
	// before the FIRST round.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outcome, err := r.RunPerFile(ctx, msg.Wrap([]llm.Message{llm.NewTextMessage("user", "review this")}), scope())
	if err != nil {
		t.Fatalf("RunPerFile: %v", err)
	}
	if outcome.State != OutcomeCompleted {
		t.Errorf("outcome = %+v, want completed", outcome)
	}
	if len(client.requests) != 1 {
		t.Fatalf("expected 1 round, got %d", len(client.requests))
	}
	if !strings.Contains(lastUserMessage(client.requests[0]), "BUDGET NEARLY EXHAUSTED") {
		t.Errorf("wrap-up prompt not injected; last user msg: %q", lastUserMessage(client.requests[0]))
	}
	for _, w := range r.Warnings() {
		if w.Type == "unit_incomplete" {
			t.Errorf("completed chain must not be marked incomplete: %+v", w)
		}
	}
}

func TestWrapUp_RoundBudgetForcesVerdictTurn(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResp("code_search", `{"search_text":"x"}`), // round 1 (3 left after)
		toolCallResp("code_search", `{"search_text":"y"}`), // round 2 (2 left → wrap-up fires before round 3)
		toolCallResp("task_done", `{}`),                    // round 3: obeys wrap-up
	}}
	r := newWrapUpRunner(client, 4)

	outcome, err := r.RunPerFile(context.Background(), msg.Wrap([]llm.Message{llm.NewTextMessage("user", "review this")}), scope())
	if err != nil {
		t.Fatalf("RunPerFile: %v", err)
	}
	if outcome.State != OutcomeCompleted {
		t.Errorf("outcome = %+v, want completed", outcome)
	}
	if len(client.requests) != 3 {
		t.Fatalf("expected 3 rounds, got %d", len(client.requests))
	}
	if strings.Contains(lastUserMessage(client.requests[1]), "BUDGET NEARLY EXHAUSTED") {
		t.Errorf("wrap-up fired too early (round 2)")
	}
	if !strings.Contains(lastUserMessage(client.requests[2]), "BUDGET NEARLY EXHAUSTED") {
		t.Errorf("wrap-up not injected at round-budget reserve; last user msg: %q", lastUserMessage(client.requests[2]))
	}
	for _, w := range r.Warnings() {
		if w.Type == "unit_incomplete" {
			t.Errorf("completed chain must not be marked incomplete: %+v", w)
		}
	}
}

func TestWrapUp_NoTaskDoneRecordsIncomplete(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResp("code_search", `{"search_text":"x"}`), // repeated for every round
	}}
	r := newWrapUpRunner(client, 3)

	outcome, err := r.RunPerFile(context.Background(), msg.Wrap([]llm.Message{llm.NewTextMessage("user", "review this")}), scope())
	if err != nil {
		t.Fatalf("RunPerFile: %v", err)
	}
	if outcome.State != OutcomeTruncated || !strings.Contains(outcome.Reason, "tool-round budget exhausted") {
		t.Errorf("outcome = %+v, want truncated (tool-round budget exhausted)", outcome)
	}
	found := false
	for _, w := range r.Warnings() {
		if w.Type == "unit_incomplete" && w.File == "a.go" {
			found = true
			if !strings.Contains(w.Message, "tool-round budget exhausted") {
				t.Errorf("unexpected reason: %q", w.Message)
			}
		}
	}
	if !found {
		t.Errorf("chain without task_done must record unit_incomplete; warnings: %+v", r.Warnings())
	}
}
