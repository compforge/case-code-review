package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/compforge/agentgo"
)

func TestAgentGoRoutingOwnsModelIdentityAndTimeout(t *testing.T) {
	var attempts atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "backup-model" || body["reasoning_effort"] != "medium" || body["prompt_cache_key"] != "conversation" || body["max_tokens"] != float64(456) {
			t.Errorf("lost routed configuration: %v", body)
		}
		fmt.Fprint(w, `{"model":"backup-model","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`)
	}))
	defer second.Close()
	client := NewLLMRouter([]ResolvedEndpoint{
		{URL: first.URL, Model: "slow-model", Alias: "primary", MaxRetries: 5},
		{URL: second.URL, Model: "backup-model", Alias: "backup"},
	}, RoutingOptions{CallTimeout: time.Second, AttemptTimeout: 20 * time.Millisecond})
	config := agentgo.ResolveCallConfig([]agentgo.CallOption{agentgo.WithThinking(agentgo.ThinkingMedium), agentgo.WithCallPromptCacheKey("conversation"), agentgo.WithMaxTokens(456)})
	response, err := client.CompletionsWithCtx(t.Context(), ChatRequest{Model: "must-not-override-candidates", Native: &agentgo.LLMRequest{Messages: []agentgo.Message{agentgo.UserMsg("review")}, Config: &config}})
	if err != nil || response.Alias != "backup" || response.Model != "backup-model" || attempts.Load() != 1 {
		t.Fatalf("response=%+v error=%v attempts=%d", response, err, attempts.Load())
	}
}

func TestAgentGoProviderWireCacheAndTools(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			bodies := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				bodies <- body
				if protocol == "anthropic" {
					fmt.Fprint(w, `{"type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}`)
				} else {
					fmt.Fprint(w, `{"model":"test","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			client := NewLLMClient(ResolvedEndpoint{Protocol: protocol, URL: server.URL, Model: "test", Token: "fake"})
			for _, mode := range []string{"auto", "required", "none", "named"} {
				choice := &ToolChoice{Mode: mode}
				if mode == "named" {
					choice = &ToolChoice{Name: "inspect"}
				}
				response, err := client.CompletionsWithCtx(t.Context(), ChatRequest{Messages: []Message{NewTextMessage("system", "stable one"), NewTextMessage("system", "stable two"), NewTextMessage("user", "review")}, Tools: []ToolDef{{Function: FunctionDef{Name: "inspect", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}}, ToolChoice: choice})
				if err != nil {
					t.Fatal(err)
				}
				body := <-bodies
				if protocol == "anthropic" {
					system := body["system"].([]any)
					if system[0].(map[string]any)["cache_control"] != nil || system[1].(map[string]any)["cache_control"] == nil {
						t.Fatalf("system cache=%v", system)
					}
					// The system breakpoint covers the preceding tool prefix.
					if response.Usage.PromptTokens != 9 || response.Usage.TotalTokens != 10 {
						t.Fatalf("cache usage double counted: %+v", response.Usage)
					}
					want := map[string]string{"auto": "auto", "required": "any", "none": "none", "named": "tool"}[mode]
					if body["tool_choice"].(map[string]any)["type"] != want {
						t.Fatalf("choice=%v", body["tool_choice"])
					}
				} else if mode == "named" {
					if body["tool_choice"].(map[string]any)["function"].(map[string]any)["name"] != "inspect" {
						t.Fatal("named tool lost")
					}
				} else if body["tool_choice"] != mode {
					t.Fatalf("choice=%v", body["tool_choice"])
				}
			}
		})
	}
}

func TestAgentGoRoutingDoesNotRetryBudgetOrBadRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"invalid effort"}}`, 400)
	}))
	defer server.Close()
	client := NewLLMRouter([]ResolvedEndpoint{{URL: server.URL, Model: "first"}, {URL: server.URL, Model: "second"}}, RoutingOptions{})
	_, err := client.CompletionsWithCtx(t.Context(), ChatRequest{Messages: []Message{NewTextMessage("user", "review")}})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("bad request retried: calls=%d err=%v", calls.Load(), err)
	}
	budget := NewBudgetClient(client, 1, nil)
	budget.used.Store(1)
	_, err = budget.CompletionsWithCtx(t.Context(), ChatRequest{})
	if !errors.Is(err, ErrTokenBudget) || calls.Load() != 1 {
		t.Fatalf("budget admission retried: %v", err)
	}
}
