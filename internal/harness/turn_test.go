package harness

import (
	"context"
	"testing"
	"time"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestWrapUpConditionsAreIndependentAndReminderIsOnce(t *testing.T) {
	for _, test := range []struct {
		name           string
		turn, maxTurns int
		deadline       time.Duration
		budget         int64
		want           bool
	}{
		{name: "investigating", turn: 2, maxTurns: 30, deadline: 5 * time.Minute},
		{name: "investigation turns", turn: 13, maxTurns: 30, want: true},
		{name: "hard turn reserve", turn: 29, maxTurns: 30, want: true},
		{name: "deadline", turn: 2, maxTurns: 30, deadline: 30 * time.Second, want: true},
		{name: "token reserve", turn: 2, maxTurns: 30, budget: 100, want: true},
		{name: "unlimited", turn: 2, maxTurns: 30, budget: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, test.deadline)
				defer cancel()
			}
			controller := newTurnController(ExecutionSpec{
				LLMClient: llm.NewBudgetClient(&scriptedClient{}, test.budget, nil),
				MaxTurns:  test.maxTurns, WrapUpAfterTurns: 12, WrapUpPrompt: "wrap up",
			})
			turn := agentgo.BeforeTurnContext{TurnIndex: test.turn, Context: agentgo.AgentContext{Messages: []agentgo.AgentMessage{msg.Text("user", "review")}}}
			messages, err := controller.BeforeTurn(ctx, turn)
			if err != nil || (len(messages) > 0) != test.want {
				t.Fatalf("messages=%v err=%v", messages, err)
			}
			messages, err = controller.BeforeTurn(ctx, turn)
			if test.want && (err != nil || len(messages) != 0) {
				t.Fatalf("repeated reminder: %v err=%v", messages, err)
			}
		})
	}
}

func TestWrapUpForecastUsesUsageThenCompactedBaseline(t *testing.T) {
	controller := newTurnController(ExecutionSpec{MaxTokens: 20000, WrapUpPrompt: "finish", ToolDefs: []llm.ToolDef{toolDef("read_files")}})
	response, err := toAgentGoResponse(toolCallResponse("read_files", `{}`, &llm.UsageInfo{PromptTokens: 30000, CompletionTokens: 8000}), nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.observeUsage(&llm.UsageInfo{CompletionTokens: 8000})
	controller.observeUsage(&llm.UsageInfo{CompletionTokens: 10})
	investigation, wrapUp := controller.forecast([]agentgo.AgentMessage{response})
	if investigation < 38000 || wrapUp < 46000 {
		t.Fatalf("forecast ignored recent usage: %d/%d", investigation, wrapUp)
	}
	compactedInvestigation, compactedWrapUp := controller.forecast([]agentgo.AgentMessage{msg.Text("user", "short committed summary")})
	if compactedInvestigation >= investigation || compactedWrapUp >= wrapUp {
		t.Fatalf("compact did not refresh estimate: %d/%d", compactedInvestigation, compactedWrapUp)
	}
	if compactedInvestigation < 8000 {
		t.Fatal("recent short reply erased output reserve")
	}
	controller.observeUsage(&llm.UsageInfo{CompletionTokens: 10})
	controller.observeUsage(&llm.UsageInfo{CompletionTokens: 10})
	decayed, _ := controller.forecast([]agentgo.AgentMessage{msg.Text("user", "short committed summary")})
	if decayed >= compactedInvestigation || decayed < wrapUpOutputFloor {
		t.Fatalf("recent output window/floor failed: %d", decayed)
	}
}
