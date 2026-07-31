package runner

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
)

func TestReviewCompressionPromptsAdaptTemplateContext(t *testing.T) {
	system, instruction := reviewCompressionPrompts(Args{
		Template: template.Template{
			MemoryCompressionTask: template.LlmConversation{
				Messages: []template.ChatMessage{
					{Role: "system", Content: "preserve confirmed findings"},
					{Role: "user", Content: "{{context}}"},
				},
			},
		},
	})
	if system != "preserve confirmed findings" {
		t.Fatalf("system prompt = %q", system)
	}
	if strings.Contains(instruction, "{{context}}") ||
		!strings.Contains(instruction, "conversation supplied above") {
		t.Fatalf("instruction was not adapted for agentcore: %q", instruction)
	}
}

func TestUnitExecutorRunsHarnessAndAggregatesFacts(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(_ context.Context, args map[string]any) (string, error) {
		return "echo:" + args["value"].(string), nil
	}))
	registry.Freeze()
	client := &unitScriptedClient{responses: []*llm.ChatResponse{
		unitToolResponse("call-1", "echo", `{"value":"ok"}`, "route-a", 7),
		unitToolResponse("call-2", "task_done", `{}`, "route-a", 3),
	}}
	history := &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)}
	executor := newUnitExecutor(Args{
		LLMClient: client,
		Model:     "review-model",
		Tools:     registry,
		MainToolDefs: []llm.ToolDef{
			unitToolDef("echo"),
			unitToolDef("task_done"),
		},
		Session: history,
		Template: template.Template{
			MaxToolRequestTimes: 2,
			MaxTokens:           1_000,
		},
	}, nil, nil)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "completed" {
		t.Fatalf("unexpected outcome: %+v", outcome)
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
	if records := history.Scopes["unit-1"].TaskRecords[session.MainTask]; len(records) != 2 {
		t.Fatalf("main task records = %d, want 2", len(records))
	}
}

func TestUnitExecutorRecordsIncompleteReview(t *testing.T) {
	client := &unitScriptedClient{responses: []*llm.ChatResponse{
		unitTextResponse("finished without task_done"),
	}}
	executor := newUnitExecutor(Args{
		LLMClient: client,
		Template: template.Template{
			MaxToolRequestTimes: 1,
			MaxTokens:           1_000,
		},
		Session: &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
	}, nil, nil)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Paths: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "truncated" || outcome.Reason != "tool-round budget exhausted" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	warnings := executor.Warnings()
	if len(warnings) != 1 || warnings[0].Type != "unit_incomplete" {
		t.Fatalf("unexpected warnings: %+v", warnings)
	}
}

func TestUnitExecutorAdaptsBoardWithoutExposingItToHarness(t *testing.T) {
	sharedBoard := board.New()
	sharedBoard.Register("unit-1", board.Interest{Paths: map[string]bool{"a.go": true}})
	sharedBoard.Publish(board.Bulletin{
		From: "unit-2", Turn: 1, Level: board.LevelConfirmed,
		Paths: []string{"a.go"}, Text: "peer read the caller",
	})
	client := &unitScriptedClient{responses: []*llm.ChatResponse{
		unitToolResponse("call-1", "post_bulletin", `{"text":"check the callee","paths":["a.go"]}`, "", 0),
		unitToolResponse("call-2", "task_done", `{}`, "", 0),
	}}
	executor := newUnitExecutor(Args{
		LLMClient: client,
		MainToolDefs: []llm.ToolDef{
			unitToolDef("post_bulletin"),
			unitToolDef("task_done"),
		},
		Features: feature.Set{feature.PostBulletin: true},
		Template: template.Template{
			MaxToolRequestTimes: 2,
			MaxTokens:           1_000,
		},
		Session: &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
	}, nil, sharedBoard)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Paths: []string{"a.go"}})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.BoardPulled != 1 || outcome.BoardInjectedTokens == 0 || outcome.BoardPosted != 1 {
		t.Fatalf("unexpected board outcome: %+v", outcome)
	}

	var foundOwnPost bool
	for _, post := range sharedBoard.Posted() {
		if post.From == "unit-1" && post.Text == "check the callee" {
			foundOwnPost = true
		}
	}
	if !foundOwnPost {
		t.Fatal("post_bulletin was not adapted into a Runner-owned board post")
	}
}

type unitScriptedClient struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
}

func (c *unitScriptedClient) CompletionsWithCtx(
	_ context.Context,
	_ llm.ChatRequest,
) (*llm.ChatResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func unitToolDef(name string) llm.ToolDef {
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

func unitToolResponse(id, name, arguments, alias string, totalTokens int64) *llm.ChatResponse {
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

func unitTextResponse(text string) *llm.ChatResponse {
	return &llm.ChatResponse{
		Choices: []llm.Choice{{
			Message:      llm.ResponseMessage{Role: "assistant", Content: &text},
			FinishReason: "stop",
		}},
	}
}
