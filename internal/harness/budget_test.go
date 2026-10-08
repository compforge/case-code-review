package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
	"github.com/qiankunli/case-code-review/internal/harness/compactor"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecutionBudgetRetainsAcceptedResultsAndStopsNextTurn(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{toolCallResponse("submit_result", `{}`, &llm.UsageInfo{PromptTokens: 100})}}
	accepted := 0
	history := session.New(t.TempDir(), "main", "test", session.SessionOptions{})
	defer history.Finalize()
	result, err := runExecution(t.Context(), ExecutionSpec{
		LLMClient: llm.NewBudgetClient(client, 100, nil), Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, MaxTurns: 5,
		ToolDefs: []llm.ToolDef{toolDef("submit_result"), toolDef("task_done")}, Session: history,
		Scope: session.Scope{ID: "u", Kind: "unit"},
		ToolHandler: toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
			if request.Tool.Name() == "submit_result" {
				accepted++
				return tool.TaskCheckpoint{}, true
			}
			return tool.TaskCheckpoint{}, false
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeTruncated || result.Reason != llm.ErrTokenBudget.Error() || accepted != 1 || len(client.Requests()) != 1 {
		t.Fatalf("result=%+v accepted=%d calls=%d", result, accepted, len(client.Requests()))
	}
	if history.LLMFailures() != 0 {
		t.Fatalf("budget rejection counted as provider failure: %d", history.LLMFailures())
	}
}

func TestContextCountsToolArgumentsAndDoesNotDoubleCountCacheCreation(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"body": strings.Repeat("x", 40000)})
	response := toolCallResponse("submit_result", string(args), &llm.UsageInfo{PromptTokens: 100, CacheWriteTokens: 30, CompletionTokens: 10000})
	message, err := toAgentGoResponse(response, nil)
	if err != nil {
		t.Fatal(err)
	}
	manager := newContextManager(ExecutionSpec{ContextWindow: 20000}, nil)
	view, err := manager.Compact(t.Context(), agentgo.TransformContext{Messages: []agentgo.AgentMessage{message}}, agentgo.CompactReasonThreshold)
	if err != nil {
		t.Fatal(err)
	}
	want := 100 + agentcontext.EstimateTokens(message)
	if view.Usage.Tokens != want || view.Usage.Tokens < 10000 {
		t.Fatalf("usage=%+v want=%d", view.Usage, want)
	}
	tokens, used, trailing := manager.EstimateContext(view.Messages)
	if tokens != want || used != 100 || trailing != agentcontext.EstimateTokens(message) {
		t.Fatalf("estimator=%d/%d/%d", tokens, used, trailing)
	}
}

func TestCompressionBudgetStopsBeforeCallingSummaryModel(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{toolCallResponse("task_done", `{}`, &llm.UsageInfo{PromptTokens: 100})}}
	budget := llm.NewBudgetClient(client, 100, nil)
	if _, err := budget.CompletionsWithCtx(t.Context(), llm.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	execution, err := NewExecution(ExecutionSpec{LLMClient: budget, Messages: []agentgo.AgentMessage{msg.Text("user", strings.Repeat("source ", 2000)), msg.Text("assistant", "reviewed"), msg.Text("user", "continue")}, MaxTurns: 5, ContextWindow: 100})
	if err != nil {
		t.Fatal(err)
	}
	// Force the summary stage to ensure budget exhaustion is not swallowed as a
	// best-effort compression failure followed by another main model request.
	execution.contextManager.engine = agentcontext.NewEngine(agentcontext.EngineConfig{ContextWindow: 100, ReserveTokens: 20,
		Compactor: compactor.NewSummaryCompactor(compactor.SummaryConfig{Model: &chatModel{client: budget, recorder: execution.recorder, taskType: session.MemoryCompressionTask}})})
	_, err = execution.contextManager.Compact(t.Context(), agentgo.TransformContext{Messages: []agentgo.AgentMessage{msg.Text("user", strings.Repeat("source ", 2000)), msg.Text("assistant", "reviewed"), msg.Text("user", "continue")}}, agentgo.CompactReasonThreshold)
	if !errors.Is(err, llm.ErrTokenBudget) || len(client.Requests()) != 1 {
		t.Fatalf("compression error=%v calls=%d", err, len(client.Requests()))
	}
}

// Replay the reported per-turn costs from #119. Its original turn 13 close was
// rejected at admission. The controller must close on turn 12 while calls are
// still admitted, even though the twelve-investigation-turn limit has not fired.
func TestTokenBudgetClosesBeforeInvestigationLimit(t *testing.T) {
	costs := [][2]int64{{7624, 1529}, {8824, 1068}, {11276, 309}, {14235, 1483}, {15858, 3862}, {20868, 1982}, {22240, 3075}, {26687, 3780}, {31765, 3084}, {33947, 3964}, {35653, 6573}}
	client := &scriptedClient{}
	for i, cost := range costs {
		client.responses = append(client.responses, toolCallResponseID(fmt.Sprintf("call-%d", i), "read_files", `{"reads":[{"file_path":"pkg/a.go"}]}`, &llm.UsageInfo{PromptTokens: cost[0], CompletionTokens: cost[1]}))
	}
	client.responses = append(client.responses, toolCallResponse("submit_result", `{}`, &llm.UsageInfo{PromptTokens: 38000, CompletionTokens: 500}))
	budget := llm.NewBudgetClient(client, 300000, nil)
	provider := &fileReadProvider{body: "ok"}
	registry := tool.NewRegistry()
	registry.Register(provider)
	registry.Freeze()
	accepted := 0
	result, err := runExecution(t.Context(), ExecutionSpec{
		LLMClient: budget, Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, MaxTurns: 30,
		ToolDefs: []llm.ToolDef{toolDef("read_files"), toolDef("submit_result")}, Tools: registry,
		WrapUpPrompt: "stop investigating now", WrapUpAfterTurns: 12, WrapUpAllowedTools: []string{"submit_result"}, NaturalCompletion: true,
		ToolHandler: toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
			if request.Tool.Name() != "submit_result" {
				return tool.TaskCheckpoint{}, false
			}
			accepted++
			return tool.CompleteWith("accepted"), true
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := client.Requests()
	if result.State != OutcomeCompleted || len(requests) != 12 || provider.calls != 11 || accepted != 1 {
		t.Fatalf("result=%+v requests=%d reads=%d accepted=%d", result, len(requests), provider.calls, accepted)
	}
	for i, request := range requests {
		if strings.Contains(requestText(request), "stop investigating now") != (i == 11) {
			t.Fatalf("wrong close timing on turn %d", i+1)
		}
	}
	assertStableToolRequests(t, requests)
	if remaining, limited := budget.Remaining(); !limited || remaining <= 0 {
		t.Fatalf("remaining=%d limited=%v", remaining, limited)
	}
}

func TestAlreadyExhaustedBudgetDoesNotInventCompletion(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{toolCallResponse("read_files", `{}`, &llm.UsageInfo{PromptTokens: 100})}}
	budget := llm.NewBudgetClient(client, 100, nil)
	if _, err := budget.CompletionsWithCtx(t.Context(), llm.ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	result, err := runExecution(t.Context(), ExecutionSpec{
		LLMClient: budget, Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, MaxTurns: 30,
		WrapUpPrompt: "finish now", NaturalCompletion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeTruncated || result.Reason != llm.ErrTokenBudget.Error() || len(client.Requests()) != 1 {
		t.Fatalf("result=%+v requests=%d", result, len(client.Requests()))
	}
}
