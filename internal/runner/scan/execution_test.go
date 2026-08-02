package scan

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestScanCompressionPromptsAdaptTemplateContext(t *testing.T) {
	system, instruction := scanCompressionPrompts(template.ScanTemplate{
		MemoryCompressionTask: template.LlmConversation{
			Messages: []template.ChatMessage{
				{Role: "system", Content: "preserve confirmed findings"},
				{Role: "user", Content: "{{context}}"},
			},
		},
	})
	if system != "preserve confirmed findings" {
		t.Fatalf("system prompt = %q", system)
	}
	if strings.Contains(instruction, "{{context}}") ||
		!strings.Contains(instruction, "conversation supplied above") {
		t.Fatalf("instruction was not adapted for agentgo: %q", instruction)
	}
}

func TestScanExecutorRunsHarnessAndAggregatesFacts(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(_ context.Context, args map[string]any) (string, error) {
		return "echo:" + args["value"].(string), nil
	}))
	registry.Freeze()
	client := &scanScriptedClient{responses: []*llm.ChatResponse{
		scanToolResponse("call-1", "echo", `{"value":"ok"}`, "route-a", 7),
		scanToolResponse("call-2", "task_done", `{}`, "route-a", 3),
	}}
	history := &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)}
	executor := newScanExecutor(Args{
		LLMClient: client,
		Model:     "scan-model",
		Tools:     registry,
		MainToolDefs: []llm.ToolDef{
			scanToolDef("echo"),
			scanToolDef("task_done"),
		},
		Session: history,
		Template: template.ScanTemplate{
			MaxToolRequestTimes: 2,
			MaxTokens:           1_000,
		},
	}, nil)

	result, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "scan"),
	}, session.Scope{ID: "a.go", Kind: "unit", Type: "file", Paths: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if executor.TotalTokensUsed() != 10 {
		t.Fatalf("total tokens = %d, want 10", executor.TotalTokensUsed())
	}
	if got := executor.ToolCalls(); got["echo"] != 1 || got["task_done"] != 0 {
		t.Fatalf("unexpected tool counts: %v", got)
	}
	if got := executor.ModelsUsed(); got["route-a"] != 2 {
		t.Fatalf("unexpected model counts: %v", got)
	}
	if records := history.Scopes["a.go"].TaskRecords[session.MainTask]; len(records) != 2 {
		t.Fatalf("main task records = %d, want 2", len(records))
	}
}

func TestScanExecutorRecordsIncompleteReview(t *testing.T) {
	client := &scanScriptedClient{responses: []*llm.ChatResponse{
		scanTextResponse("finished without task_done"),
	}}
	executor := newScanExecutor(Args{
		LLMClient: client,
		Template: template.ScanTemplate{
			MaxToolRequestTimes: 1,
			MaxTokens:           1_000,
		},
		Session: &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
	}, nil)

	result, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "scan"),
	}, session.Scope{ID: "a.go", Paths: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "truncated" || result.Reason != "tool-round budget exhausted" {
		t.Fatalf("unexpected result: %+v", result)
	}
	warnings := executor.Warnings()
	if len(warnings) != 1 || warnings[0].Type != "unit_incomplete" {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

type scanScriptedClient struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
}

func (c *scanScriptedClient) CompletionsWithCtx(
	_ context.Context,
	_ llm.ChatRequest,
) (*llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func scanToolDef(name string) llm.ToolDef {
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

func scanToolResponse(id, name, arguments, alias string, totalTokens int64) *llm.ChatResponse {
	return &llm.ChatResponse{
		Alias: alias,
		Model: "served-model",
		Choices: []llm.Choice{{
			Message: llm.ResponseMessage{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: id, Type: "function",
					Function: llm.FunctionCall{Name: name, Arguments: arguments},
				}},
			},
			FinishReason: "tool_calls",
		}},
		Usage: &llm.UsageInfo{
			PromptTokens: totalTokens,
			TotalTokens:  totalTokens,
		},
	}
}

func scanTextResponse(text string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Choices: []llm.Choice{{
			Message:      llm.ResponseMessage{Role: "assistant", Content: &text},
			FinishReason: "stop",
		}},
	}
}
