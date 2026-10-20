package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
)

func TestOpenAIReasoningSurvivesAgentGoRoundTrip(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies = append(bodies, body)
		w.Header().Set("Content-Type", "text/event-stream")
		if len(bodies) == 1 {
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", `{"model":"review-model","choices":[{"index":0,"finish_reason":"tool_calls","delta":{"role":"assistant","content":"visible answer","reasoning_content":"retained reasoning","tool_calls":[{"index":0,"id":"read-1","type":"function","function":{"name":"inspect","arguments":"{}"}}]}}],"usage":{"prompt_tokens":20,"completion_tokens":7,"total_tokens":27,"prompt_tokens_details":{"cached_tokens":12}}}`)
		} else {
			_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", `{"model":"review-model","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"done"}}],"usage":{"prompt_tokens":30,"completion_tokens":1,"total_tokens":31}}`)
		}
	}))
	defer server.Close()
	model := llm.NewModel(llm.ResolvedEndpoint{URL: server.URL, Model: "review-model"})
	execution, err := NewExecution(ExecutionSpec{ChatModel: model, MaxTokens: 16384, Messages: []agentgo.AgentMessage{agentgo.UserMsg("work")}})
	if err != nil {
		t.Fatal(err)
	}
	executor := agentgo.NewModelExecutor(agentgo.ModelExecutorConfig{Middlewares: []agentgo.ModelMiddleware{execution.recordModel()}})
	first, err := executor.Call(t.Context(), model, agentgo.ModelExecution{Request: agentgo.LLMRequest{Messages: []agentgo.Message{agentgo.UserMsg("review")}}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Message.TextContent() != "visible answer" || first.Message.ThinkingContent() != "retained reasoning" {
		t.Fatalf("response lost text/reasoning separation: %+v", first.Message.Content)
	}
	if first.Message.Usage.CacheRead != 12 {
		t.Fatalf("cache usage = %+v", first.Message.Usage)
	}
	_, err = executor.Call(t.Context(), model, agentgo.ModelExecution{Request: agentgo.LLMRequest{Messages: []agentgo.Message{
		agentgo.UserMsg("review"), first.Message, agentgo.ToolResultMsg("read-1", json.RawMessage(`"source"`), false),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	assistant := bodies[1]["messages"].([]any)[1].(map[string]any)
	if assistant["content"] != "visible answer" || assistant["reasoning_content"] != "retained reasoning" {
		t.Fatalf("wire message = %#v", assistant)
	}
	for _, body := range bodies {
		if body["stream"] != true {
			t.Fatal("provider request is not streaming")
		}
		if body["max_tokens"] != float64(16384) {
			t.Fatalf("output budget = %#v", body["max_tokens"])
		}
	}
}

func TestExecutionKeepsContextLargerThanOutputBudget(t *testing.T) {
	client := &scriptedClient{responses: []*agentgo.LLMResponse{textResponse("done")}}
	source := strings.Repeat("specific source evidence\n", 1000)
	result, err := runExecution(t.Context(), ExecutionSpec{
		ChatModel: client, Messages: []agentgo.AgentMessage{msg.Text("user", source)},
		MaxTokens: 512, ContextWindow: 200000, MaxTurns: 1, NaturalCompletion: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("state = %s", result.State)
	}
	requests := client.Requests()
	if len(requests) != 1 || requests[0].Config.MaxTokens != 512 || requests[0].Messages[0].TextContent() != source {
		t.Fatal("output cap changed the input context budget")
	}
}

// +case:id=sdk_exploration_timeout_recovers,desc=`OpenAI SDK wraps an HTTP request deadline while awaiting a response`,expect=`the failed call remains recorded and wrap-up succeeds in a new Run before the caller deadline`
func TestSDKExplorationTimeoutResumesWrapUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	history := recording.New(t.TempDir(), "main", "test", recording.SessionOptions{})
	defer history.Finalize()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}

		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			<-r.Context().Done()
			return
		}
		text := ""
		for _, message := range body.Messages {
			text += message.Content
		}
		if !strings.Contains(text, "finish review now") {
			t.Error("wrap-up request missing completion prompt")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", `{"model":"review-model","choices":[{"index":0,"finish_reason":"stop","delta":{"role":"assistant","content":"review complete"}}]}`)
	}))
	defer server.Close()
	client := llm.NewModel(llm.ResolvedEndpoint{URL: server.URL, Model: "review-model", Timeout: 5 * time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := runExecution(ctx, ExecutionSpec{ChatModel: client, Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, Session: history, Scope: recording.Scope{ID: "unit-1", Kind: "unit"}, MaxTurns: 10, WrapUpAt: time.Now().Add(time.Second), WrapUpPrompt: "finish review now", NaturalCompletion: true})
	if err != nil || result.State != OutcomeCompleted || calls.Load() != 2 {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	history.Finalize()
	path, _ := history.TranscriptPath()
	transcript, err := recording.ReadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	var failures, responses int
	runs := map[string]bool{}
	for _, record := range transcript.Records {
		switch record["type"] {
		case "llm_error":
			failures++
			failure, _ := record["failure"].(map[string]any)
			if failure["code"] != "caller_deadline_exceeded" {
				t.Fatalf("failure=%v", record)
			}
		case "llm_response":
			responses++
		case "llm_request":
			ref := record["session_ref"].(map[string]any)
			runs[ref["run_id"].(string)] = true
		}
	}
	if failures != 1 || responses != 1 || len(runs) != 2 {
		t.Fatalf("failures=%d responses=%d runs=%v", failures, responses, runs)
	}
}
