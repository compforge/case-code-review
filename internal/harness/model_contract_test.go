package harness

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/compforge/agentgo"
)

func TestExecutionReportsLengthAndPreservesEffortUsage(t *testing.T) {
	zero, one := 0, 1
	for _, limit := range []int{zero, one} {
		response := &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.ThinkingBlock("partial")}, StopReason: agentgo.StopReasonLength, Usage: &agentgo.Usage{Input: 4, Output: 32, Reasoning: 32, TotalTokens: 36}}}

		client := &scriptedClient{responses: []*agentgo.LLMResponse{response, response}}
		result, err := runExecution(t.Context(), ExecutionSpec{ChatModel: client, Messages: []agentgo.AgentMessage{agentgo.UserMsg("work")}, NaturalCompletion: true, MaxTurns: 10, MaxLengthRecoveries: &limit, ReasoningEffort: "max"})
		if err != nil || result.State != OutcomeTruncated || result.Reason != "length" {
			t.Fatalf("result=%+v error=%v", result, err)
		}
		requests := client.Requests()
		if len(requests) != limit+1 || result.Usage.Reasoning != 32*(limit+1) || result.Usage.TotalTokens != 36*(limit+1) {
			t.Fatalf("requests=%d usage=%+v", len(requests), result.Usage)
		}
		for _, request := range requests {
			if request.Config.ThinkingLevel != "max" {
				t.Fatalf("effort=%q", request.Config.ThinkingLevel)
			}
		}
	}
}

func TestSummaryLengthFailureIsTruncatedRatherThanLLMError(t *testing.T) {
	execution, err := NewExecution(ExecutionSpec{ChatModel: &scriptedClient{}, Messages: []agentgo.AgentMessage{agentgo.UserMsg("work")}})
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

// +case:id=native_model_passthrough,desc=`the Harness receives native replay state and call settings`,expect=`only lossless tool argument repair changes the response; history and configuration remain intact`
func TestHarnessPreservesNativeHistoryAndCallSettings(t *testing.T) {
	state := &agentgo.ProviderState{Provider: "test", Model: "model", Data: json.RawMessage(`{"signature":"opaque"}`)}
	thinking := agentgo.ThinkingBlock("retained reasoning")
	thinking.ProviderState = state
	response := &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{thinking, agentgo.TextBlock("partial")}, StopReason: agentgo.StopReasonLength, Usage: &agentgo.Usage{Input: 4, Output: 9, Reasoning: 7}, Metadata: map[string]any{"route_endpoint": "primary"}}}
	client := &scriptedClient{responses: []*agentgo.LLMResponse{response}}
	execution, err := NewExecution(ExecutionSpec{ChatModel: client, Messages: []agentgo.AgentMessage{agentgo.UserMsg("work")}})
	if err != nil {
		t.Fatal(err)
	}
	executor := agentgo.NewModelExecutor(agentgo.ModelExecutorConfig{Middlewares: []agentgo.ModelMiddleware{execution.recordModel()}})
	messages := []agentgo.Message{response.Message, agentgo.UserMsg("continue")}
	config := agentgo.CallConfig{ThinkingLevel: "xhigh", ThinkingBudget: 128, MaxTokens: 256, PromptCacheKey: "stable-prefix", SessionID: "session", ToolChoice: "none", ResponseFormat: &agentgo.ResponseFormat{Type: agentgo.ResponseFormatJSONObject}}
	result, err := executor.Call(t.Context(), client, agentgo.ModelExecution{Request: agentgo.LLMRequest{Messages: messages, Config: &config}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Message.Content, response.Message.Content) || !reflect.DeepEqual(result.Message.Usage, response.Message.Usage) {
		t.Fatalf("native response changed: %+v", result)
	}
	request := client.Requests()[0]
	if !reflect.DeepEqual(request.Messages, messages) || !reflect.DeepEqual(request.Config, &config) {
		t.Fatalf("native request changed: %+v", request)
	}
}
