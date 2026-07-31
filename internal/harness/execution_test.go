package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecuteCompletesWithTaskDone(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("task_done", `{}`, &llm.UsageInfo{
			PromptTokens:     10,
			CompletionTokens: 2,
			TotalTokens:      12,
		}),
	}}

	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient: client,
		Model:     "review-model",
		Messages:  []msg.Msg{msg.Text("user", "review this unit")},
		ToolDefs:  []llm.ToolDef{toolDef("task_done")},
		MaxTurns:  1,
		MaxTokens: 512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("state = %q, want %q", result.State, OutcomeCompleted)
	}
	if result.Turns != 1 || result.ToolCalls != 1 {
		t.Fatalf("unexpected runtime facts: %+v", result)
	}
	if result.Usage.TotalTokens != 12 {
		t.Fatalf("usage = %+v", result.Usage)
	}

	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if requests[0].Model != "review-model" || requests[0].MaxTokens != 512 {
		t.Fatalf("request did not preserve model config: %+v", requests[0])
	}
}

func TestExecuteRequiresTaskDoneBeforeNaturalStop(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		textResponse("I am finished."),
		toolCallResponse("task_done", `{}`, nil),
	}}

	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review this unit")},
		ToolDefs:  []llm.ToolDef{toolDef("task_done")},
		MaxTurns:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Turns != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}

	requests := client.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	last := requests[1].Messages[len(requests[1].Messages)-1]
	if last.Role != "user" || last.ExtractText() != defaultCompletionPrompt {
		t.Fatalf("completion guard message = %#v", last)
	}
}

func TestExecuteReportsTurnBudgetAsTruncation(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		textResponse("I am finished."),
	}}

	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review this unit")},
		MaxTurns:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeTruncated || result.Reason != "max_turns" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestExecuteAdaptsRegistryTools(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(_ context.Context, args map[string]any) (string, error) {
		return "echo:" + args["value"].(string), nil
	}))
	registry.Freeze()

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("echo", `{"value":"ok"}`, nil),
		toolCallResponse("task_done", `{}`, nil),
	}}
	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "use echo")},
		ToolDefs: []llm.ToolDef{
			toolDef("echo"),
			toolDef("task_done"),
		},
		Tools:    registry,
		MaxTurns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}

	requests := client.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	var found bool
	for _, message := range requests[1].Messages {
		if message.Role == "tool" && message.ExtractText() == "echo:ok" {
			found = true
		}
	}
	if !found {
		t.Fatalf("second request did not contain adapted tool result: %#v", requests[1].Messages)
	}
}

func TestExecuteConnectsHandlerSessionAndEvents(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("code_comment", `{"path":"a.go"}`, &llm.UsageInfo{TotalTokens: 7}),
		toolCallResponse("task_done", `{}`, &llm.UsageInfo{TotalTokens: 3}),
	}}
	client.responses[0].Alias = "route-a"

	history := &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)}
	scope := session.Scope{
		ID:    "unit-1",
		Kind:  "unit",
		Type:  "func",
		Paths: []string{"a.go"},
	}
	var handled ToolRequest
	handler := toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
		if request.Tool.Name() != "code_comment" {
			return tool.TaskCheckpoint{}, false
		}
		handled = request
		return tool.Of("submitted"), true
	})
	var events []ExecutionEvent

	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient:   client,
		Messages:    []msg.Msg{msg.Text("user", "review this unit")},
		ToolDefs:    []llm.ToolDef{toolDef("code_comment"), toolDef("task_done")},
		ToolHandler: handler,
		Session:     history,
		Scope:       scope,
		Events: EventSinkFunc(func(event ExecutionEvent) {
			events = append(events, event)
		}),
		MaxTurns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Usage.TotalTokens != 10 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if handled.Scope.ID != scope.ID || handled.Alias != "route-a" {
		t.Fatalf("handler context was not preserved: %+v", handled)
	}

	records := history.Scopes[scope.ID].TaskRecords[session.MainTask]
	if len(records) != 2 {
		t.Fatalf("task records = %d, want 2", len(records))
	}
	if records[0].Response == nil || len(records[0].Response.ToolCalls) != 1 {
		t.Fatalf("first response was not recorded: %+v", records[0])
	}
	if len(records[0].ToolResults) != 1 ||
		records[0].ToolResults[0].ToolName != "code_comment" ||
		records[0].ToolResults[0].Result != "submitted" {
		t.Fatalf("tool result was not joined to its model turn: %+v", records[0].ToolResults)
	}

	counts := make(map[ExecutionEventType]int)
	for _, event := range events {
		counts[event.Type]++
	}
	if counts[EventModelResponse] != 2 ||
		counts[EventToolStart] != 2 ||
		counts[EventToolEnd] != 2 ||
		counts[EventExecutionEnd] != 1 {
		t.Fatalf("unexpected event stream: counts=%v events=%+v", counts, events)
	}
}

func TestExecuteContextDeduplicatesFileReads(t *testing.T) {
	body := fmt.Sprintf(
		"File: pkg/a.go (Total lines: 3)\nIS_TRUNCATED: false\nLINE_RANGE: 1-3\n%s",
		"1|package a\n2|\n3|func F() {}\n",
	)
	registry := tool.NewRegistry()
	registry.Register(fileReadProvider{body: body})
	registry.Freeze()

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "file_read", `{"file_path":"pkg/a.go"}`, nil),
		toolCallResponseID("call-2", "file_read", `{"file_path":"pkg/a.go"}`, nil),
		toolCallResponseID("call-3", "task_done", `{}`, nil),
	}}
	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review this unit")},
		ToolDefs: []llm.ToolDef{
			toolDef("file_read"),
			toolDef("task_done"),
		},
		Tools:            registry,
		MaxTurns:         3,
		ContextWindow:    10_000,
		FileDedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}

	requests := client.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(requests))
	}
	var full, stubbed int
	for _, message := range requests[2].Messages {
		if message.Role != "tool" {
			continue
		}
		text := message.ExtractText()
		switch {
		case strings.Contains(text, "superseded"):
			stubbed++
		case strings.Contains(text, "func F() {}"):
			full++
		}
	}
	if stubbed != 1 || full != 1 {
		t.Fatalf("want one stubbed and one full file read, got stubbed=%d full=%d", stubbed, full)
	}
}

func TestExecuteContextEvictsWithoutMutatingInput(t *testing.T) {
	content := "File: pkg/large.go (Total lines: 200)\n" + strings.Repeat("1|some code content\n", 200)
	file := msg.NewFile("pkg/large.go", 1, 200, 200, content)
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("task_done", `{}`, nil),
	}}

	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient:        client,
		Messages:         []msg.Msg{msg.Text("user", "review"), file},
		ToolDefs:         []llm.ToolDef{toolDef("task_done")},
		MaxTurns:         1,
		ContextWindow:    100,
		FileEvictEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if file.Stubbed() {
		t.Fatal("context projection mutated the caller's message")
	}

	requests := client.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	var evicted bool
	for _, message := range requests[0].Messages {
		if strings.Contains(message.ExtractText(), "elided to fit the context budget") {
			evicted = true
		}
	}
	if !evicted {
		t.Fatalf("projected request did not evict the large file: %#v", requests[0].Messages)
	}
}

func TestExecuteUsesAgentcoreSummaryAndRecordsItsUsage(t *testing.T) {
	long := strings.Repeat("review evidence and reasoning ", 30)
	messages := make([]msg.Msg, 0, 8)
	for range 8 {
		messages = append(messages, msg.Text("user", long))
	}
	client := &scriptedClient{responses: []*llm.ChatResponse{
		textResponse("<summary>preserve the confirmed review facts</summary>"),
		toolCallResponse("task_done", `{}`, &llm.UsageInfo{TotalTokens: 7}),
	}}
	client.responses[0].Usage = &llm.UsageInfo{TotalTokens: 5}
	history := &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)}

	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient:     client,
		Messages:      messages,
		ToolDefs:      []llm.ToolDef{toolDef("task_done")},
		Session:       history,
		Scope:         session.Scope{ID: "unit-1"},
		MaxTurns:      1,
		MaxTokens:     400,
		ContextWindow: 400,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Usage.TotalTokens != 12 {
		t.Fatalf("unexpected result: %+v", result)
	}
	scope := history.Scopes["unit-1"]
	if len(scope.TaskRecords[session.MemoryCompressionTask]) != 1 {
		t.Fatalf("compression records = %d, want 1", len(scope.TaskRecords[session.MemoryCompressionTask]))
	}
	if len(scope.TaskRecords[session.MainTask]) != 1 {
		t.Fatalf("main records = %d, want 1", len(scope.TaskRecords[session.MainTask]))
	}
	requests := client.Requests()
	if len(requests) != 2 || !strings.Contains(requestText(requests[1]), "preserve the confirmed review facts") {
		t.Fatalf("main request did not use the context summary: %#v", requests)
	}
}

func TestExecuteInjectsWrapUpBeforeTurnBudgetEnds(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(context.Context, map[string]any) (string, error) {
		return "ok", nil
	}))
	registry.Freeze()
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "echo", `{}`, nil),
		toolCallResponseID("call-2", "echo", `{}`, nil),
		toolCallResponseID("call-3", "task_done", `{}`, nil),
	}}

	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review")},
		ToolDefs: []llm.ToolDef{
			toolDef("echo"),
			toolDef("task_done"),
		},
		Tools:        registry,
		MaxTurns:     3,
		WrapUpPrompt: "wrap up now",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}

	requests := client.Requests()
	if strings.Contains(requestText(requests[0]), "wrap up now") {
		t.Fatal("wrap-up was injected before the reserved turns")
	}
	if !strings.Contains(requestText(requests[1]), "wrap up now") ||
		!strings.Contains(requestText(requests[2]), "wrap up now") {
		t.Fatal("wrap-up must be injected and retained for the final two turns")
	}
}

func TestExecuteCommitsIncrementalTurnContext(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(context.Context, map[string]any) (string, error) {
		return "ok", nil
	}))
	registry.Freeze()
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "echo", `{}`, nil),
		toolCallResponseID("call-2", "task_done", `{}`, nil),
	}}

	pulls := 0
	turnContext := turnContextFunc(func(_ context.Context, scope session.Scope) []msg.Msg {
		pulls++
		if scope.ID != "unit-1" || pulls > 1 {
			return nil
		}
		return []msg.Msg{msg.NewBoard("peer confirmed the call path")}
	})
	result, err := Execute(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review")},
		ToolDefs: []llm.ToolDef{
			toolDef("echo"),
			toolDef("task_done"),
		},
		Tools:       registry,
		Scope:       session.Scope{ID: "unit-1"},
		TurnContext: turnContext,
		MaxTurns:    2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}

	requests := client.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for i, request := range requests {
		if !strings.Contains(requestText(request), "peer confirmed the call path") {
			t.Fatalf("request %d lost committed turn context: %#v", i, request.Messages)
		}
	}
}

type toolHandlerFunc func(context.Context, ToolRequest) (tool.TaskCheckpoint, bool)

func (f toolHandlerFunc) HandleTool(ctx context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
	return f(ctx, request)
}

type turnContextFunc func(context.Context, session.Scope) []msg.Msg

func (f turnContextFunc) PullTurnContext(ctx context.Context, scope session.Scope) []msg.Msg {
	return f(ctx, scope)
}

type fileReadProvider struct{ body string }

func (p fileReadProvider) Tool() tool.Tool { return tool.FileRead }
func (p fileReadProvider) Execute(context.Context, map[string]any) (string, error) {
	return p.body, nil
}

type scriptedClient struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	requests  []llm.ChatRequest
}

func (c *scriptedClient) CompletionsWithCtx(_ context.Context, request llm.ChatRequest) (*llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, request)
	if len(c.responses) == 0 {
		panic("scriptedClient: unexpected request")
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func (c *scriptedClient) Requests() []llm.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]llm.ChatRequest(nil), c.requests...)
}

func toolDef(name string) llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name: name,
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

func toolCallResponse(name, arguments string, usage *llm.UsageInfo) *llm.ChatResponse {
	return toolCallResponseID("call-1", name, arguments, usage)
}

func toolCallResponseID(id, name, arguments string, usage *llm.UsageInfo) *llm.ChatResponse {
	return &llm.ChatResponse{
		Model: "served-model",
		Choices: []llm.Choice{{
			Message: llm.ResponseMessage{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID:   id,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      name,
						Arguments: arguments,
					},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: usage,
	}
}

func textResponse(text string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Choices: []llm.Choice{{
			Message:      llm.ResponseMessage{Role: "assistant", Content: &text},
			FinishReason: "stop",
		}},
	}
}

func requestText(request llm.ChatRequest) string {
	var out strings.Builder
	for _, message := range request.Messages {
		out.WriteString(message.ExtractText())
	}
	return out.String()
}
