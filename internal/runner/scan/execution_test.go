package scan

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
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
	client := &scanScriptedClient{responses: []*agentgo.LLMResponse{
		scanToolResponse("call-1", "echo", `{"value":"ok"}`, "route-a", 7),
		scanToolResponse("call-2", "task_done", `{}`, "route-a", 3),
	}}
	t.Setenv("HOME", t.TempDir())
	history := recording.New(t.TempDir(), "test", "test", recording.SessionOptions{})
	defer history.Finalize()
	executor := newScanExecutor(Args{
		ChatModel: client,
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

	result, err := executor.Run(context.Background(), []agentgo.AgentMessage{
		msg.Text("user", "scan"),
	}, recording.Scope{ID: "a.go", Kind: "unit", Type: "file", Paths: []string{"a.go"}})
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
	if records := recordedRequests(t, history); len(records) != 2 {
		t.Fatalf("main task records = %d, want 2", len(records))
	}
}

func TestScanExecutorRecordsIncompleteReview(t *testing.T) {
	client := &scanScriptedClient{responses: []*agentgo.LLMResponse{
		scanTextResponse("finished without task_done"),
	}}
	executor := newScanExecutor(Args{
		ChatModel: client,
		Template: template.ScanTemplate{
			MaxToolRequestTimes: 1,
			MaxTokens:           1_000,
		},
		Session: &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
	}, nil)

	result, err := executor.Run(context.Background(), []agentgo.AgentMessage{
		msg.Text("user", "scan"),
	}, recording.Scope{ID: "a.go", Paths: []string{"a.go"}})
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
	responses []*agentgo.LLMResponse
}

func (c *scanScriptedClient) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
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

func scanToolResponse(id, name, arguments, alias string, totalTokens int64) *agentgo.LLMResponse {
	return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: append([]agentgo.ContentBlock{}, agentgo.ToolCallBlock(agentgo.ToolCall{ID: id, Name: name, Args: json.RawMessage(arguments)})), StopReason: agentgo.StopReasonToolUse, Usage: &agentgo.Usage{Input: int(totalTokens), TotalTokens: int(totalTokens)}, Metadata: map[string]any{"model": "served-model",
		"route_endpoint": alias}}}
}

func scanTextResponse(text string) *agentgo.LLMResponse {
	return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{
		agentgo.TextBlock(text)}, StopReason: agentgo.StopReasonStop}}
}

func recordedRequests(t *testing.T, review *recording.Review) []map[string]any {
	t.Helper()
	review.Finalize()
	path, err := review.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := recording.ReadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	var records []map[string]any
	for _, r := range transcript.Records {
		if r["type"] == "llm_request" {
			records = append(records, r)
		}
	}
	return records
}

func (c *scanScriptedClient) SupportsTools() bool { return true }
func (c *scanScriptedClient) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return llm.CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return c.Generate(ctx, messages, tools, opts...) })
}
