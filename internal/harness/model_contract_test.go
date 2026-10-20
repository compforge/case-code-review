package harness

import (
	"fmt"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecutionReportsLengthAndPreservesEffortUsage(t *testing.T) {
	zero, one := 0, 1
	for _, limit := range []int{zero, one} {
		response := &llm.ChatResponse{Choices: []llm.Choice{{FinishReason: "length", Message: llm.ResponseMessage{Role: "assistant", ReasoningContent: "partial"}}}, Usage: &llm.UsageInfo{PromptTokens: 4, CompletionTokens: 32, ReasoningTokens: 32, TotalTokens: 36}}
		client := &scriptedClient{responses: []*llm.ChatResponse{response, response}}
		result, err := runExecution(t.Context(), ExecutionSpec{LLMClient: client, Messages: []agentgo.AgentMessage{agentgo.UserMsg("work")}, NaturalCompletion: true, MaxTurns: 10, MaxLengthRecoveries: &limit, ReasoningEffort: "max"})
		if err != nil || result.State != OutcomeTruncated || result.Reason != "length" {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		requests := client.Requests()
		if len(requests) != limit+1 || result.Usage.ReasoningTokens != int64(32*(limit+1)) || result.Usage.TotalTokens != int64(36*(limit+1)) {
			t.Fatalf("requests=%d usage=%+v", len(requests), result.Usage)
		}
		for _, request := range requests {
			if request.ReasoningEffort != "max" {
				t.Fatalf("effort=%q", request.ReasoningEffort)
			}
		}
	}
}

func TestSummaryLengthFailureIsTruncatedRatherThanLLMError(t *testing.T) {
	execution, err := NewExecution(ExecutionSpec{LLMClient: &scriptedClient{}, Messages: []agentgo.AgentMessage{agentgo.UserMsg("work")}})
	if err != nil {
		t.Fatal(err)
	}
	execution.runErr = fmt.Errorf("history summary: %w", agentgo.ErrOutputTruncated)
	execution.summary = &agentgo.RunSummary{EndReason: agentgo.EndReasonError}
	result, err := execution.finish(t.Context())
	if err != nil || result.State != OutcomeTruncated || result.Reason != "length" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
