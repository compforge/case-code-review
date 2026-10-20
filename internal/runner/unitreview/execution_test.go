package unitreview

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
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
	client := &unitScriptedClient{responses: []*agentgo.LLMResponse{
		unitToolResponse("call-1", "echo", `{"value":"ok"}`, "route-a", 7),
		unitToolResponse("call-2", "submit_hypothesis", `{"path":"a.go","content":"issue","existing_code":"x","trigger":"input","impact":"failure","change_attribution":"changed here","evidence":["a.go:1"],"uncertainty":"","category":"bug","severity":"high"}`, "route-a", 3),
		unitTextResponse("No material lead remains."),
	}}
	t.Setenv("HOME", t.TempDir())
	history := recording.New(t.TempDir(), "test", "test", recording.SessionOptions{})
	defer history.Finalize()
	executor := NewExecutor(ExecutorConfig{
		ChatModel: client,
		Model:     "review-model",
		Tools:     registry,
		ToolDefs: []llm.ToolDef{
			unitToolDef("echo"),
		},
		Session:   history,
		MaxTurns:  10,
		MaxTokens: 1_000,
	}, &HypothesisHook{}, nil)

	outcome, err := executor.Run(context.Background(), []agentgo.AgentMessage{
		msg.Text("user", "review"),
	}, recording.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}}, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "completed" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if executor.TotalTokensUsed() != 10 {
		t.Fatalf("total tokens = %d, want 10", executor.TotalTokensUsed())
	}
	if got := executor.ToolCalls(); got["echo"] != 1 || got["submit_hypothesis"] != 1 {
		t.Fatalf("unexpected tool counts: %v", got)
	}
	if got := executor.ModelsUsed(); got["route-a"] != 2 {
		t.Fatalf("unexpected model counts: %v", got)
	}
	if records := recordedRequests(t, history); len(records) != 3 {
		t.Fatalf("main task records = %d, want 3", len(records))
	}
}

func TestUnitExecutorSubmitsHypothesesIncrementally(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(context.Context, map[string]any) (string, error) {
		return "next lead checked", nil
	}))
	registry.Freeze()
	client := &unitScriptedClient{responses: []*agentgo.LLMResponse{
		unitToolResponse("submit-1", "submit_hypothesis", `{"path":"a.go","content":"first issue","existing_code":"x","trigger":"first trigger","impact":"first failure","change_attribution":"changed here","evidence":["a.go:1"],"uncertainty":"","category":"bug","severity":"high"}`, "route-a", 1),
		unitToolResponse("check-2", "echo", `{}`, "route-a", 1),
		unitToolResponse("submit-3", "submit_hypothesis", `{"path":"a.go","content":"second issue","existing_code":"y","trigger":"second trigger","impact":"second failure","change_attribution":"changed here","evidence":["a.go:2"],"uncertainty":"","category":"bug","severity":"medium"}`, "route-a", 1),
		unitTextResponse("No material lead remains."),
	}}
	var hypotheses []Hypothesis
	hook := &HypothesisHook{OnResolved: func(h Hypothesis) { hypotheses = append(hypotheses, h) }}
	executor := NewExecutor(ExecutorConfig{
		ChatModel: client,
		Tools:     registry,
		ToolDefs:  []llm.ToolDef{unitToolDef("echo")},
		MaxTurns:  10,
		MaxTokens: 1_000,
		Session:   &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
	}, hook, nil)

	outcome, err := executor.Run(context.Background(), []agentgo.AgentMessage{
		msg.Text("user", "review"),
	}, recording.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}}, nil, time.Time{})
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
	client := &unitScriptedClient{responses: []*agentgo.LLMResponse{
		unitTextResponse("The supplied diff has no material defect mechanism."),
	}}
	t.Setenv("HOME", t.TempDir())
	history := recording.New(t.TempDir(), "test", "test", recording.SessionOptions{})
	defer history.Finalize()
	executor := NewExecutor(ExecutorConfig{
		ChatModel: client,
		MaxTurns:  10,
		MaxTokens: 1_000,
		Session:   history,
	}, &HypothesisHook{}, nil)

	outcome, err := executor.Run(context.Background(), []agentgo.AgentMessage{
		msg.Text("user", "review"),
	}, recording.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}}, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	records := recordedRequests(t, history)
	if outcome.State != "completed" || len(records) != 1 {
		t.Fatalf("outcome=%+v records=%d", outcome, len(records))
	}
}

func TestUnitExecutorReturnsIncompleteOutcome(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewBuiltin(tool.Named("echo"), func(context.Context, map[string]any) (string, error) {
		return "ok", nil
	}))
	registry.Freeze()
	client := &unitScriptedClient{responses: []*agentgo.LLMResponse{
		unitToolResponse("call-1", "echo", `{}`, "", 0),
	}}
	executor := NewExecutor(ExecutorConfig{
		ChatModel: client,
		Tools:     registry,
		ToolDefs: []llm.ToolDef{
			unitToolDef("echo"),
		},
		MaxTurns:  1,
		MaxTokens: 1_000,
		Session:   &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
	}, nil, nil)

	outcome, err := executor.Run(context.Background(), []agentgo.AgentMessage{
		msg.Text("user", "review"),
	}, recording.Scope{ID: "unit-1", Paths: []string{"a.go"}}, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State != "truncated" || outcome.Reason != "tool-round budget exhausted" {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if warnings := executor.Warnings(); len(warnings) != 0 {
		t.Fatalf("R1 duplicated the pipeline terminal warning: %+v", warnings)
	}
}

func TestUnitExecutorAdaptsBoardWithoutExposingItToHarness(t *testing.T) {
	sharedBoard := board.New()
	sharedBoard.Register("unit-1", board.Interest{Paths: map[string]bool{"a.go": true}})
	sharedBoard.Publish(board.Bulletin{
		From: "unit-2", Turn: 1, Level: board.LevelConfirmed,
		Paths: []string{"a.go"}, Text: "peer read the caller",
	})
	client := &unitScriptedClient{responses: []*agentgo.LLMResponse{
		unitToolResponse("call-1", "post_bulletin", `{"text":"check the callee","paths":["a.go"]}`, "", 0),
		unitTextResponse("No material lead remains."),
	}}
	executor := NewExecutor(ExecutorConfig{
		ChatModel: client,
		ToolDefs: []llm.ToolDef{
			unitToolDef("post_bulletin"),
		},
		PostBulletin: true,
		MaxTurns:     3,
		MaxTokens:    1_000,
		Session:      &recording.Review{Scopes: make(map[string]*recording.ReviewScope)},
	}, &HypothesisHook{}, sharedBoard)

	outcome, err := executor.Run(context.Background(), []agentgo.AgentMessage{
		msg.Text("user", "review"),
	}, recording.Scope{ID: "unit-1", Paths: []string{"a.go"}}, nil, time.Time{})
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
		scope:    recording.Scope{ID: "chain", Paths: []string{"a.go", "b.go"}},
		turn:     2,
	}
	run.publishToolFact(SubmitHypothesis.Name(), []byte(`{}`))
	if posts := sharedBoard.Posted(); len(posts) != 0 {
		t.Fatalf("an unassessed hypothesis must not become a confirmed board fact: %+v", posts)
	}
}

type unitScriptedClient struct {
	mu        sync.Mutex
	responses []*agentgo.LLMResponse
}

func (c *unitScriptedClient) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
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

func unitToolResponse(id, name, arguments, alias string, totalTokens int64) *agentgo.LLMResponse {
	return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: append([]agentgo.ContentBlock{}, agentgo.ToolCallBlock(agentgo.ToolCall{ID: id, Name: name, Args: json.RawMessage(arguments)})), StopReason: agentgo.StopReason(
		"tool_calls",
	), Usage: &agentgo.Usage{Input: int(totalTokens), TotalTokens: int(totalTokens)}, Metadata: map[string]any{"model": "served-model",
		"route_endpoint": alias,
	}}}
}

func unitTextResponse(text string) *agentgo.LLMResponse {
	return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.TextBlock(text)}, StopReason: agentgo.StopReason(
		"stop",
	)}}
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

func (c *unitScriptedClient) SupportsTools() bool { return true }
func (c *unitScriptedClient) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return llm.CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return c.Generate(ctx, messages, tools, opts...) })
}
