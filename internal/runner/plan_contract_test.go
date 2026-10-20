package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
)

func TestPlanRejectsLengthAndForwardsTaskEffort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	history := recording.New(t.TempDir(), "main", "test", recording.SessionOptions{})
	defer history.Finalize()
	client := timedReviewClient(func(_ context.Context, request agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
		if request.Config.ThinkingLevel != "minimal" {
			t.Fatalf("effort=%q", request.Config.ThinkingLevel)
		}
		response := reviewText("unfinished plan")
		response.Message.StopReason = "length"
		response.Message.Usage = &agentgo.Usage{Output: 32, Reasoning: 30, TotalTokens: 32}
		return response, nil
	})
	runner := &Runner{args: Args{ChatModel: client, Template: template.Template{PlanTask: &template.LlmConversation{ReasoningEffort: "minimal", Messages: []template.ChatMessage{{Role: "user", Content: "plan {{diff}}"}}}}}, session: history, executor: unitreview.NewExecutor(unitreview.ExecutorConfig{}, nil, nil)}
	text, err := runner.executePlanPhase(t.Context(), recording.Scope{ID: "plan-1", Kind: "file"}, "diff", "", "rule")
	if text != "" || !errors.Is(err, llm.ErrOutputTruncated) {
		t.Fatalf("text=%q error=%v", text, err)
	}
}
