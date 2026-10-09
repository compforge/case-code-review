package harness

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestOpenAIReasoningSurvivesAgentGoRoundTrip(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = w.Write([]byte(`{"model":"review-model","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"visible answer","reasoning_content":"retained reasoning","tool_calls":[{"id":"read-1","type":"function","function":{"name":"inspect","arguments":"{}"}}]}}],"usage":{"prompt_tokens":20,"completion_tokens":7,"total_tokens":27,"prompt_tokens_details":{"cached_tokens":12}}}`))
		} else {
			_, _ = w.Write([]byte(`{"model":"review-model","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":30,"completion_tokens":1,"total_tokens":31}}`))
		}
	}))
	defer server.Close()
	model := &chatModel{
		client: llm.NewOpenAIClient(llm.ClientConfig{URL: server.URL, Model: "review-model"}),
		model:  "review-model", maxTokens: 16384,
		recorder: newExecutionRecorder(ExecutionSpec{}, "roundtrip"),
	}
	first, err := model.Generate(t.Context(), []agentgo.Message{agentgo.UserMsg("review")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Message.TextContent() != "visible answer" || first.Message.ThinkingContent() != "retained reasoning" {
		t.Fatalf("response lost text/reasoning separation: %+v", first.Message.Content)
	}
	if first.Message.Usage.CacheRead != 12 {
		t.Fatalf("cache usage = %+v", first.Message.Usage)
	}
	_, err = model.Generate(t.Context(), []agentgo.Message{
		agentgo.UserMsg("review"), first.Message, agentgo.ToolResultMsg("read-1", json.RawMessage(`"source"`), false),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	assistant := bodies[1]["messages"].([]any)[1].(map[string]any)
	if assistant["content"] != "visible answer" || assistant["reasoning_content"] != "retained reasoning" {
		t.Fatalf("wire message = %#v", assistant)
	}
	for _, body := range bodies {
		if body["max_completion_tokens"] != float64(16384) {
			t.Fatalf("output budget = %#v", body["max_completion_tokens"])
		}
	}
}

func TestExecutionKeepsContextLargerThanOutputBudget(t *testing.T) {
	client := &scriptedClient{responses: []*llm.ChatResponse{textResponse("done")}}
	source := strings.Repeat("specific source evidence\n", 1000)
	result, err := runExecution(t.Context(), ExecutionSpec{
		LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", source)},
		MaxTokens: 512, ContextWindow: 200000, MaxTurns: 1, NaturalCompletion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("state = %s", result.State)
	}
	requests := client.Requests()
	if len(requests) != 1 || requests[0].MaxTokens != 512 || requests[0].Messages[0].ExtractText() != source {
		t.Fatal("output cap changed the input context budget")
	}
}
