package unitreview

import (
	"context"
	"sync"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestAttachToolResultPreservesSuccessfulRepositorySnapshots(t *testing.T) {
	readResult := func(path string) string {
		return tool.EncodeFileReadResults([]string{"File: " + path + " (Total lines: 1)\nIS_TRUNCATED: false\nLINE_RANGE: 1-1\n1|x"})
	}
	reviewUnit := unit.UnitOf(unit.Fragment{Path: "target.go"})
	AttachToolResult(&reviewUnit, tool.FileRead.Name(), []byte(`{"reads":[{"file_path":"a.go"}]}`), readResult("a.go"))
	snapshots := reviewUnit.Review().FileSnapshots
	if len(snapshots) != 1 || snapshots[0].Path != "a.go" || snapshots[0].Kind != unit.CurrentSnapshot || snapshots[0].Content == "" {
		t.Fatalf("snapshots = %+v", snapshots)
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
		unitToolResponse("call-2", "submit_hypotheses", `{"hypotheses":[]}`, "route-a", 3),
		unitTextResponse("No material lead remains."),
	}}
	history := &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)}
	executor := NewExecutor(ExecutorConfig{
		LLMClient: client,
		Model:     "review-model",
		Tools:     registry,
		ToolDefs: []llm.ToolDef{
			unitToolDef("echo"),
		},
		Session:   history,
		MaxTurns:  10,
		MaxTokens: 1_000,
	}, &HypothesisHook{}, nil)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "completed" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if executor.TotalTokensUsed() != 10 {
		t.Fatalf("total tokens = %d, want 10", executor.TotalTokensUsed())
	}
	if got := executor.ToolCalls(); got["echo"] != 1 || got["submit_hypotheses"] != 1 {
		t.Fatalf("unexpected tool counts: %v", got)
	}
	if got := executor.ModelsUsed(); got["route-a"] != 2 {
		t.Fatalf("unexpected model counts: %v", got)
	}
	if records := history.Scopes["unit-1"].TaskRecords[session.MainTask]; len(records) != 3 {
		t.Fatalf("main task records = %d, want 3", len(records))
	}
}

func TestUnitExecutorSubmitsHypothesesIncrementally(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(context.Context, map[string]any) (string, error) {
		return "next lead checked", nil
	}))
	registry.Freeze()
	client := &unitScriptedClient{responses: []*llm.ChatResponse{
		unitToolResponse("submit-1", "submit_hypotheses", `{"hypotheses":[{"path":"a.go","content":"first issue","existing_code":"x","trigger":"first trigger","impact":"first failure","change_attribution":"changed here","evidence":["a.go:1"],"uncertainty":"","category":"bug","severity":"high"}]}`, "route-a", 1),
		unitToolResponse("check-2", "echo", `{}`, "route-a", 1),
		unitToolResponse("submit-3", "submit_hypotheses", `{"hypotheses":[{"path":"a.go","content":"second issue","existing_code":"y","trigger":"second trigger","impact":"second failure","change_attribution":"changed here","evidence":["a.go:2"],"uncertainty":"","category":"bug","severity":"medium"}]}`, "route-a", 1),
		unitTextResponse("No material lead remains."),
	}}
	var hypotheses []Hypothesis
	hook := &HypothesisHook{OnResolved: func(h Hypothesis) { hypotheses = append(hypotheses, h) }}
	executor := NewExecutor(ExecutorConfig{
		LLMClient: client,
		Tools:     registry,
		ToolDefs:  []llm.ToolDef{unitToolDef("echo")},
		MaxTurns:  10,
		MaxTokens: 1_000,
		Session:   &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
	}, hook, nil)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "completed" || len(hypotheses) != 2 {
		t.Fatalf("outcome=%+v hypotheses=%+v", outcome, hypotheses)
	}
	if hypotheses[0].Content != "first issue" || hypotheses[1].Content != "second issue" {
		t.Fatalf("incremental hypotheses = %+v", hypotheses)
	}
}

func TestUnitExecutorCompletesSimpleReviewImmediately(t *testing.T) {
	client := &unitScriptedClient{responses: []*llm.ChatResponse{
		unitTextResponse("The supplied diff has no material defect mechanism."),
	}}
	history := &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)}
	executor := NewExecutor(ExecutorConfig{
		LLMClient: client,
		MaxTurns:  10,
		MaxTokens: 1_000,
		Session:   history,
	}, &HypothesisHook{}, nil)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	records := history.Scopes["unit-1"].TaskRecords[session.MainTask]
	if outcome.State != "completed" || len(records) != 1 {
		t.Fatalf("outcome=%+v records=%d", outcome, len(records))
	}
}

func TestUnitExecutorRecordsIncompleteReview(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(context.Context, map[string]any) (string, error) {
		return "ok", nil
	}))
	registry.Freeze()
	client := &unitScriptedClient{responses: []*llm.ChatResponse{
		unitToolResponse("call-1", "echo", `{}`, "", 0),
	}}
	executor := NewExecutor(ExecutorConfig{
		LLMClient: client,
		Tools:     registry,
		ToolDefs: []llm.ToolDef{
			unitToolDef("echo"),
		},
		MaxTurns:  1,
		MaxTokens: 1_000,
		Session:   &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
	}, nil, nil)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Paths: []string{"a.go"}}, nil)
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
		unitToolResponse("call-2", "submit_hypotheses", `{"hypotheses":[]}`, "", 0),
	}}
	executor := NewExecutor(ExecutorConfig{
		LLMClient: client,
		ToolDefs: []llm.ToolDef{
			unitToolDef("post_bulletin"),
		},
		PostBulletin: true,
		MaxTurns:     3,
		MaxTokens:    1_000,
		Session:      &session.SessionHistory{Scopes: make(map[string]*session.ScopeSession)},
	}, &HypothesisHook{}, sharedBoard)

	outcome, err := executor.Run(context.Background(), []msg.Msg{
		msg.Text("user", "review"),
	}, session.Scope{ID: "unit-1", Paths: []string{"a.go"}}, nil)
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

func TestUnitExecutionDoesNotPublishHypothesisAsConfirmedFact(t *testing.T) {
	sharedBoard := board.New()
	run := &unitExecution{
		executor: &Executor{board: sharedBoard},
		scope:    session.Scope{ID: "chain", Paths: []string{"a.go", "b.go"}},
		turn:     2,
	}
	run.publishToolFact(SubmitHypotheses.Name(), []byte(`{"hypotheses":[]}`))
	if posts := sharedBoard.Posted(); len(posts) != 0 {
		t.Fatalf("an unassessed hypothesis must not become a confirmed board fact: %+v", posts)
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
