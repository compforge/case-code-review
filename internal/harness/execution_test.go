package harness

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecutionCompletesWithTaskDone(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("task_done", `{}`, &llm.UsageInfo{
			PromptTokens:     10,
			CompletionTokens: 2,
			TotalTokens:      12,
		}),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
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

func TestExecutionCompletesWithDomainTerminalTool(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("submit-1", "submit_result", `{"items":[]}`, nil),
	}}
	handler := toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
		if request.Tool.Name() != "submit_result" {
			return tool.TaskCheckpoint{}, false
		}
		return tool.CompleteWith("Result accepted."), true
	})

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient:        client,
		Messages:         []msg.Msg{msg.Text("user", "produce a result")},
		ToolDefs:         []llm.ToolDef{toolDef("submit_result")},
		ToolHandler:      handler,
		CompletionTool:   "submit_result",
		CompletionPrompt: "Call submit_result to complete.",
		MaxTurns:         1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Turns != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	requests := client.Requests()
	if len(requests) != 1 || len(requests[0].Tools) != 1 || requests[0].Tools[0].Function.Name != "submit_result" {
		t.Fatalf("custom terminal tool set = %+v", requests)
	}
}

func TestExecutionKeepsRunningAfterRejectedTerminalSubmission(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("submit-1", "submit_result", `{"items":[{}]}`, nil),
		toolCallResponseID("submit-2", "submit_result", `{"items":[]}`, nil),
	}}
	calls := 0
	handler := toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
		if request.Tool.Name() != "submit_result" {
			return tool.TaskCheckpoint{}, false
		}
		calls++
		if calls == 1 {
			return tool.Of("Error: invalid result"), true
		}
		return tool.CompleteWith("Result accepted."), true
	})

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient:      client,
		Messages:       []msg.Msg{msg.Text("user", "produce a result")},
		ToolDefs:       []llm.ToolDef{toolDef("submit_result")},
		ToolHandler:    handler,
		CompletionTool: "submit_result",
		MaxTurns:       2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Turns != 2 || calls != 2 {
		t.Fatalf("unexpected result: %+v calls=%d", result, calls)
	}
	requests := client.Requests()
	if len(requests) != 2 || !strings.Contains(requestText(requests[1]), "Error: invalid result") {
		t.Fatalf("rejected receipt was not preserved: %+v", requests)
	}
}

func TestNewExecutionValidatesInputAndRunsOnce(t *testing.T) {
	if _, err := NewExecution(ExecutionSpec{}); err == nil {
		t.Fatal("missing LLM client must be rejected at construction")
	}
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("task_done", `{}`, nil),
	}}
	execution, err := NewExecution(ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review")},
		MaxTurns:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := execution.Run(context.Background()); err == nil {
		t.Fatal("an Execution must not be reused")
	}
}

func TestExecutionContinuesFromPriorCommittedContext(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("done-1", "task_done", `{}`, nil),
		toolCallResponseID("done-2", "task_done", `{}`, nil),
	}}
	first, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "first hypothesis")},
		ToolDefs:  []llm.ToolDef{toolDef("task_done")},
		MaxTurns:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient:    client,
		Messages:     []msg.Msg{msg.Text("user", "second hypothesis")},
		ToolDefs:     []llm.ToolDef{toolDef("task_done")},
		MaxTurns:     1,
		ContinueFrom: &first,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != OutcomeCompleted {
		t.Fatalf("second execution = %+v", second)
	}
	requests := client.Requests()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	text := requestText(requests[1])
	if !strings.Contains(text, "first hypothesis") || !strings.Contains(text, "second hypothesis") {
		t.Fatalf("continued request lost Lane context: %q", text)
	}
}

func TestExecutionRequiresTaskDoneBeforeNaturalStop(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		textResponse("I am finished."),
		toolCallResponse("task_done", `{}`, nil),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
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

func TestExecutionAllowsConfiguredNaturalCompletion(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		textResponse("No material work remains."),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient:         client,
		Messages:          []msg.Msg{msg.Text("user", "review this unit")},
		MaxTurns:          3,
		NaturalCompletion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Turns != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	requests := client.Requests()
	if len(requests) != 1 || len(requests[0].Tools) != 0 {
		t.Fatalf("natural completion unexpectedly injected a terminal tool: %+v", requests)
	}
}

func TestExecutionStopsAfterAcceptedWrapUpResultInNaturalMode(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(context.Context, map[string]any) (string, error) {
		return "ok", nil
	}))
	registry.Freeze()
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "echo", `{}`, nil),
		toolCallResponseID("call-2", "submit_result", `{"items":[]}`, nil),
	}}
	handler := toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
		if request.Tool.Name() != "submit_result" {
			return tool.TaskCheckpoint{}, false
		}
		return tool.CompleteWith("Result accepted."), true
	})

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review")},
		ToolDefs: []llm.ToolDef{
			toolDef("echo"), toolDef("submit_result"),
		},
		Tools:              registry,
		ToolHandler:        handler,
		MaxTurns:           10,
		WrapUpPrompt:       "submit ready results now",
		WrapUpAfterTurns:   1,
		WrapUpAllowedTools: []string{"submit_result"},
		NaturalCompletion:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Turns != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestExecutionRejectsTaskDoneUntilDomainCompletion(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("done-1", "task_done", `{}`, nil),
		toolCallResponseID("done-2", "task_done", `{}`, nil),
	}}
	checks := 0
	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "assess every item")},
		ToolDefs:  []llm.ToolDef{toolDef("task_done")},
		MaxTurns:  2,
		CompletionCheck: func(context.Context) (bool, string) {
			checks++
			return checks > 1, "one item is still missing"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || result.Turns != 2 {
		t.Fatalf("result = %+v, want completion on second task_done", result)
	}
	requests := client.Requests()
	if len(requests) != 2 || !strings.Contains(requests[1].Messages[len(requests[1].Messages)-1].ExtractText(), "one item is still missing") {
		t.Fatalf("rejection was not returned to the model: %+v", requests)
	}
}

func TestExecutionReportsTurnBudgetAsTruncation(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{
		textResponse("I am finished."),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
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

func TestExecutionAdaptsRegistryTools(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(_ context.Context, args map[string]any) (string, error) {
		return "echo:" + args["value"].(string), nil
	}))
	registry.Freeze()

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("echo", `{"value":"ok"}`, nil),
		toolCallResponse("task_done", `{}`, nil),
	}}
	result, err := runExecution(context.Background(), ExecutionSpec{
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

func TestExecutionConnectsHandlerSessionAndEvents(t *testing.T) {
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

	result, err := runExecution(context.Background(), ExecutionSpec{
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

func TestExecutionSkipsFileReadAlreadyCoveredByEarlierRead(t *testing.T) {
	body := fmt.Sprintf(
		"File: pkg/a.go (Total lines: 3)\nIS_TRUNCATED: false\nLINE_RANGE: 1-3\n%s",
		"1|package a\n2|\n3|func F() {}\n",
	)
	registry := tool.NewRegistry()
	provider := &fileReadProvider{body: body}
	registry.Register(provider)
	registry.Freeze()

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("call-2", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("call-3", "task_done", `{}`, nil),
	}}
	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review this unit")},
		ToolDefs: []llm.ToolDef{
			toolDef("read_files"),
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
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", provider.calls)
	}
	var full, covered int
	for _, message := range requests[2].Messages {
		if message.Role != "tool" {
			continue
		}
		text := message.ExtractText()
		switch {
		case strings.Contains(text, "Already available in the current context from an earlier read_files result"):
			covered++
		case strings.Contains(text, "func F() {}"):
			full++
		}
	}
	if covered != 1 || full != 1 {
		t.Fatalf("want one covered notice and one full file read, got covered=%d full=%d", covered, full)
	}
}

func TestDomainMessageDelegatesApplicationPriority(t *testing.T) {
	domain := domainMessage{value: msg.NewFile("a.go", 1, 1, 1, "1|x").ConfigurePriority(42)}
	if got := domain.Priority(); got != 42 {
		t.Fatalf("domain priority = %d, want 42", got)
	}
}

func TestContextPromotesFileReadResultBackToDomainMessage(t *testing.T) {
	result := tool.EncodeFileReadResults([]string{
		"File: pkg/a.go (Total lines: 3)\nIS_TRUNCATED: false\nLINE_RANGE: 1-3\n1|package a\n",
	})
	wire := wireToAgentMessage(llm.NewToolResultMessage("call-1", result))
	wire.Metadata = map[string]any{
		"tool_name":    msg.FileReadToolName,
		"tool_call_id": "call-1",
	}

	normalized, _ := normalizeContextMessages([]agentgo.AgentMessage{wire})
	if len(normalized) != 1 {
		t.Fatalf("normalized messages = %d", len(normalized))
	}
	domain, ok := normalized[0].(domainMessage)
	if !ok {
		t.Fatalf("read_files result stayed wire-shaped: %T", normalized[0])
	}
	batch, ok := domain.value.(*msg.FileBatch)
	if !ok || len(batch.Files()) != 1 {
		t.Fatalf("promoted message = %#v", domain.value)
	}
	file := batch.Files()[0]
	if file.Path != "pkg/a.go" || file.Start != 1 || file.End != 3 ||
		batch.ToLLM(msg.CompactionNone).ToolCallID != "call-1" {
		t.Fatalf("promoted message = %#v", domain.value)
	}
}

func TestContextUsesToolCallArgumentsWhenPromotingResult(t *testing.T) {
	assistant := wireToAgentMessage(llm.NewToolCallMessage("", []llm.ToolCall{{
		ID: "search-1", Type: "function",
		Function: llm.FunctionCall{Name: msg.CodeSearchToolName, Arguments: `{"searches":[{"query":"NewExecution","syntax":"literal"}]}`},
	}}))
	result := wireToAgentMessage(llm.NewToolResultMessage(
		"search-1", tool.EncodeCodeSearchResults([]string{
			"File: internal/harness/execution.go\nMatch lines: 1\n10|func NewExecution\n",
		}),
	))
	result.Metadata = map[string]any{"tool_call_id": "search-1"}

	normalized, changed := normalizeContextMessages([]agentgo.AgentMessage{assistant, result})
	if !changed || len(normalized) != 2 {
		t.Fatalf("normalized=%d changed=%t", len(normalized), changed)
	}
	domain := normalized[1].(domainMessage)
	search, ok := domain.value.(*msg.SearchBatch)
	if !ok || len(search.Results()) != 1 || search.Results()[0].Query != "NewExecution" {
		t.Fatalf("promoted search = %#v", domain.value)
	}
}

func TestBaselineFileDoesNotCoverCurrentFileRead(t *testing.T) {
	result := "Baseline ref: abc123\nFile: pkg/a.go (Total lines: 3)\nIS_TRUNCATED: false\nLINE_RANGE: 1-3\n1|old\n"
	baseline := &msg.File{}
	if !baseline.FromLLM(msg.LLMToolResult{
		Tool: msg.FileReadBaseToolName, ToolCallID: "base-1", Content: result,
	}) {
		t.Fatal("baseline result was not promoted")
	}
	manager := newContextManager(ExecutionSpec{FileDedupEnabled: true}, nil)
	projection, err := manager.Project(context.Background(), wrapDomainMessages([]msg.Msg{baseline}))
	if err != nil {
		t.Fatal(err)
	}
	manager.remember(nil, projection.Messages, projection.Usage, "test", false)
	if _, covered := manager.coveredFileRead(tool.FileReadRequest{FilePath: "pkg/a.go"}); covered {
		t.Fatal("baseline source must not suppress a current-snapshot read_files")
	}
}

func TestFileContextAdmissionDoesNotSuppressSourceRead(t *testing.T) {
	contextMessage := msg.NewFileContext([]msg.FileContextEntry{
		{Path: "pkg/a.go", View: msg.ViewOutline, Reason: "callee", Ref: "pkg/a.go::A", Content: "File outline: pkg/a.go"},
		{Path: "pkg/b.go", View: msg.ViewReference, Reason: "usage_site"},
	})
	manager := newContextManager(ExecutionSpec{FileDedupEnabled: true}, nil)
	if _, covered := manager.coveredFileRead(tool.FileReadRequest{FilePath: "pkg/a.go"}); covered {
		t.Fatal("outline navigation must not suppress an exact source read")
	}
	items := agentgo.CollectContextItems(wrapDomainMessages([]msg.Msg{contextMessage}))
	if len(items) != 2 || items[0].Identity != "pkg/a.go" || items[0].Representation != "outline" ||
		items[0].Reason != "callee" || items[0].Ref != "pkg/a.go::A" ||
		items[1].Identity != "pkg/b.go" || items[1].Representation != "reference" ||
		items[1].Reason != "usage_site" {
		t.Fatalf("projected items = %#v", items)
	}
}

func TestExecutionSkipsFileReadAlreadyCoveredByPreload(t *testing.T) {
	body := "File: pkg/a.go (Total lines: 3)\n1|package a\n2|\n3|func F() {}\n"
	registry := tool.NewRegistry()
	provider := &fileReadProvider{body: body}
	registry.Register(provider)
	registry.Freeze()

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("call-2", "task_done", `{}`, nil),
	}}
	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages: []msg.Msg{
			msg.Text("user", "review this unit"),
			msg.NewFile("pkg/a.go", 1, 3, 3, body).
				ConfigurePresentation("code under review", ""),
		},
		ToolDefs:         []llm.ToolDef{toolDef("read_files"), toolDef("task_done")},
		Tools:            registry,
		MaxTurns:         2,
		ContextWindow:    10_000,
		FileDedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || provider.calls != 0 {
		t.Fatalf("result=%+v provider calls=%d, want completed without executing read_files", result, provider.calls)
	}
	if !strings.Contains(requestText(client.Requests()[1]), "Already available in the current context from the initial source context") {
		t.Fatalf("second request lacks preload coverage notice: %#v", client.Requests()[1].Messages)
	}
	if first := requestText(client.Requests()[0]); !strings.Contains(first, "Available file content already present") ||
		!strings.Contains(first, "pkg/a.go lines 1-3 — code under review") {
		t.Fatalf("first request lacks visible-file inventory: %q", first)
	}
}

func TestExecutionRunsOnlyUncoveredMembersOfFileReadBatch(t *testing.T) {
	preload := "File: pkg/a.go (Total lines: 3)\n1|package a\n2|\n3|func A() {}\n"
	body := "File: pkg/b.go (Total lines: 2)\nIS_TRUNCATED: false\nLINE_RANGE: 1-2\n1|package b\n2|func B() {}\n"
	registry := tool.NewRegistry()
	provider := &fileReadProvider{body: body}
	registry.Register(provider)
	registry.Freeze()

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "read_files", `{"reads":[{"file_path":"pkg/a.go"},{"file_path":"pkg/b.go"}]}`, nil),
		toolCallResponseID("call-2", "task_done", `{}`, nil),
	}}
	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages: []msg.Msg{
			msg.Text("user", "review this unit"),
			msg.NewFile("pkg/a.go", 1, 3, 3, preload),
		},
		ToolDefs:         []llm.ToolDef{toolDef("read_files"), toolDef("task_done")},
		Tools:            registry,
		MaxTurns:         2,
		ContextWindow:    10_000,
		FileDedupEnabled: true,
	})
	if err != nil || result.State != OutcomeCompleted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	requests, parseErr := tool.ParseFileReadRequests(provider.args)
	if provider.calls != 1 || parseErr != nil || len(requests) != 1 || requests[0].FilePath != "pkg/b.go" {
		t.Fatalf("provider calls=%d args=%v parsed=%+v err=%v", provider.calls, provider.args, requests, parseErr)
	}
	text := requestText(client.Requests()[1])
	if !strings.Contains(text, "Already available in the current context") || !strings.Contains(text, "File: pkg/b.go") {
		t.Fatalf("merged batch result missing covered/fresh members: %q", text)
	}
}

func TestContextCompactsFromTailAndCommitsLevel(t *testing.T) {
	content := func(path string) string {
		return fmt.Sprintf("File: %s (Total lines: 80)\n%s", path, strings.Repeat("1|source evidence for review\n", 80))
	}
	messages := []agentgo.AgentMessage{
		domainMessage{value: msg.Text("system", "stable system")},
		domainMessage{value: msg.Text("user", "stable task")},
		domainMessage{value: msg.NewFile("a.go", 1, 80, 80, content("a.go"))},
		domainMessage{value: msg.NewFile("b.go", 1, 80, 80, content("b.go"))},
		domainMessage{value: msg.NewFile("c.go", 1, 80, 80, content("c.go"))},
	}
	full := countContextTokens(messages)
	tail := append([]agentgo.AgentMessage(nil), messages...)
	last := tail[len(tail)-1].(domainMessage)
	last.compaction = msg.CompactionReference
	tail[len(tail)-1] = last
	afterTail := countContextTokens(tail)
	limit := (full + afterTail) / 2
	manager := newContextManager(ExecutionSpec{
		ContextWindow:    limit * 5 / 4,
		FileEvictEnabled: true,
	}, &chatModel{client: &scriptedClient{}})

	projection, err := manager.Project(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}
	committed := projection.CommitMessages
	if !projection.ShouldCommit || len(committed) != len(messages) {
		t.Fatalf("projection did not commit compaction: %+v", projection)
	}
	if projection.Compaction == nil || projection.Compaction.Reason != agentgo.CompactReasonThreshold || !projection.Compaction.Committed {
		t.Fatalf("projection lost AgentGo compaction details: %+v", projection.Compaction)
	}
	for i := 2; i < 4; i++ {
		if got := committed[i].(domainMessage).compaction; got != msg.CompactionNone {
			t.Fatalf("message %d compacted before tail: %v", i, got)
		}
	}
	if got := committed[4].(domainMessage).compaction; got != msg.CompactionReference {
		t.Fatalf("tail compaction = %v, want reference", got)
	}

	second, err := manager.Project(context.Background(), committed)
	if err != nil {
		t.Fatal(err)
	}
	firstWire := agentgo.ToMessages(projection.CommitMessages)
	secondWire := agentgo.ToMessages(second.Messages)
	if firstWire[0].TextContent() != secondWire[0].TextContent() ||
		firstWire[1].TextContent() != secondWire[1].TextContent() {
		t.Fatal("stable prompt prefix changed after committed compaction")
	}
}

func TestExecutionWrapUpKeepsSchemasButBlocksInvestigation(t *testing.T) {
	registry := tool.NewRegistry()
	provider := &fileReadProvider{body: "unexpected"}
	registry.Register(provider)
	registry.Freeze()
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("call-2", "task_done", `{}`, nil),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient:          client,
		Messages:           []msg.Msg{msg.Text("user", "review")},
		ToolDefs:           []llm.ToolDef{toolDef("read_files"), toolDef("task_done")},
		Tools:              registry,
		MaxTurns:           2,
		WrapUpPrompt:       "wrap up now",
		WrapUpAllowedTools: []string{"task_done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted || provider.calls != 0 {
		t.Fatalf("result=%+v provider calls=%d", result, provider.calls)
	}
	requests := client.Requests()
	if len(requests) != 2 || len(requests[0].Tools) != len(requests[1].Tools) {
		t.Fatalf("tool schemas changed across wrap-up: %#v", requests)
	}
	for i := range requests[0].Tools {
		if requests[0].Tools[i].Function.Name != requests[1].Tools[i].Function.Name {
			t.Fatalf("tool schema order changed: %#v vs %#v", requests[0].Tools, requests[1].Tools)
		}
	}
	if !strings.Contains(requestText(requests[1]), "Investigation is closed") {
		t.Fatalf("blocked tool result missing from next turn: %q", requestText(requests[1]))
	}
}

func TestExecutionWrapUpStopsAfterOneIgnoredCompletionTurn(t *testing.T) {
	registry := tool.NewRegistry()
	provider := &fileReadProvider{body: "unexpected"}
	registry.Register(provider)
	registry.Freeze()
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("call-2", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("call-3", "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, nil),
		toolCallResponseID("call-4", "task_done", `{}`, nil),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient:          client,
		Messages:           []msg.Msg{msg.Text("user", "review")},
		ToolDefs:           []llm.ToolDef{toolDef("read_files"), toolDef("task_done")},
		Tools:              registry,
		MaxTurns:           10,
		WrapUpPrompt:       "wrap up now",
		WrapUpAfterTurns:   1,
		WrapUpAllowedTools: []string{"task_done"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeTruncated || result.Reason != "wrap-up completion not submitted" {
		t.Fatalf("result = %+v, want bounded wrap-up truncation", result)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want only the pre-wrap-up read", provider.calls)
	}
	requests := client.Requests()
	if len(requests) != 3 {
		t.Fatalf("requests = %d, want investigation plus one blocked turn and one final turn", len(requests))
	}
	if !strings.Contains(requestText(requests[2]), "wrap up now") ||
		!strings.Contains(requestText(requests[2]), defaultCompletionPrompt) {
		t.Fatalf("final correction prompt missing: %q", requestText(requests[2]))
	}
}

func TestExecutionRunsFileReadWhenPreloadOnlyPartiallyCoversRange(t *testing.T) {
	body := "File: pkg/a.go (Total lines: 30)\nIS_TRUNCATED: false\nLINE_RANGE: 1-20\n1|package a\n"
	registry := tool.NewRegistry()
	provider := &fileReadProvider{body: body}
	registry.Register(provider)
	registry.Freeze()

	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("call-1", "read_files", `{"reads":[{"file_path":"pkg/a.go","start_line":1,"end_line":20}]}`, nil),
		toolCallResponseID("call-2", "task_done", `{}`, nil),
	}}
	_, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages: []msg.Msg{
			msg.Text("user", "review this unit"),
			msg.NewFile("pkg/a.go", 10, 20, 30, "File: pkg/a.go (Total lines: 30)\nLINE_RANGE: 10-20\n10|func F() {}\n"),
		},
		ToolDefs:         []llm.ToolDef{toolDef("read_files"), toolDef("task_done")},
		Tools:            registry,
		MaxTurns:         2,
		ContextWindow:    10_000,
		FileDedupEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want partial coverage to execute", provider.calls)
	}
}

func TestExecutionContextEvictsWithoutMutatingInput(t *testing.T) {
	content := "File: pkg/large.go (Total lines: 200)\n" + strings.Repeat("1|some code content\n", 200)
	file := msg.NewFile("pkg/large.go", 1, 200, 200, content)
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponse("task_done", `{}`, nil),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
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
	var compacted bool
	for _, message := range requests[0].Messages {
		if strings.Contains(message.ExtractText(), "compacted to a reference") {
			compacted = true
		}
	}
	if !compacted {
		t.Fatalf("projected request did not compact the large file: %#v", requests[0].Messages)
	}
}

func TestExecutionUsesAgentGoSummaryAndRecordsItsUsage(t *testing.T) {
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

	result, err := runExecution(context.Background(), ExecutionSpec{
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

func TestExecutionInjectsWrapUpBeforeTurnBudgetEnds(t *testing.T) {
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

	result, err := runExecution(context.Background(), ExecutionSpec{
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

func TestExecutionWrapsUpAfterPlannedInvestigationTurns(t *testing.T) {
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

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review")},
		ToolDefs: []llm.ToolDef{
			toolDef("echo"),
			toolDef("task_done"),
		},
		Tools:            registry,
		MaxTurns:         10,
		WrapUpPrompt:     "wrap up now",
		WrapUpAfterTurns: 2,
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
	if strings.Contains(requestText(requests[1]), "wrap up now") {
		t.Fatal("wrap-up was injected before two complete investigation turns")
	}
	if !strings.Contains(requestText(requests[2]), "wrap up now") {
		t.Fatal("wrap-up was not injected on the first turn after the planned investigation window")
	}
}

func TestExecutionCommitsIncrementalTurnContext(t *testing.T) {
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
	result, err := runExecution(context.Background(), ExecutionSpec{
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

func runExecution(ctx context.Context, spec ExecutionSpec) (ExecutionResult, error) {
	execution, err := NewExecution(spec)
	if err != nil {
		return ExecutionResult{}, err
	}
	return execution.Run(ctx)
}

func (f toolHandlerFunc) HandleTool(ctx context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
	return f(ctx, request)
}

type turnContextFunc func(context.Context, session.Scope) []msg.Msg

func (f turnContextFunc) PullTurnContext(ctx context.Context, scope session.Scope) []msg.Msg {
	return f(ctx, scope)
}

type fileReadProvider struct {
	body  string
	calls int
	args  map[string]any
}

func (p *fileReadProvider) Tool() tool.Tool { return tool.FileRead }
func (p *fileReadProvider) Execute(_ context.Context, args map[string]any) (string, error) {
	p.calls++
	p.args = args
	return tool.EncodeFileReadResults([]string{p.body}), nil
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
