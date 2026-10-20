package llm

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/compforge/agentgo"
)

func TestPerCallEffortReachesOpenAIWire(t *testing.T) {
	bodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response","model":"test","choices":[{"message":{"role":"assistant","reasoning_content":"partial"},"finish_reason":"length"}],"usage":{"prompt_tokens":4,"completion_tokens":32,"total_tokens":36,"completion_tokens_details":{"reasoning_tokens":32}}}`))
	}))
	defer server.Close()
	defaults := map[string]any{"thinking": map[string]any{"type": "enabled"}, "reasoning_effort": "high", "custom_flag": true}
	client := newOpenAIModel(ModelConfig{URL: server.URL, Model: "test", APIKey: "fake", ExtraBody: defaults})
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh", "max", "off", "none", "auto", ""} {
		response, err := Call(t.Context(), client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("work")}}}, Config: &agentgo.CallConfig{ThinkingLevel: agentgo.NormalizeThinkingLevel(agentgo.ThinkingLevel(effort))}})
		if err != nil {
			t.Fatal(err)
		}
		body := <-bodies
		want := effort
		switch effort {
		case "off", "none":
			want = "none"
		case "auto", "":
			want = "high"
		}
		if body["reasoning_effort"] != want || body["custom_flag"] != true {
			t.Fatalf("effort=%q body=%v", effort, body)
		}
		if effort != "" && effort != "auto" && body["thinking"] != nil {
			t.Fatal("raw thinking default overrode typed intent")
		}
		if response.Message.Usage.Reasoning != 32 || response.Message.Usage.TotalTokens != 36 || response.Message.StopReason != "length" {
			t.Fatalf("response=%+v", response)
		}
	}
	if defaults["thinking"].(map[string]any)["type"] != "enabled" || defaults["reasoning_effort"] != "high" {
		t.Fatal("shared defaults mutated")
	}
}

func TestPerCallEffortReachesAnthropicWire(t *testing.T) {
	bodies := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		bodies <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"response","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":4,"output_tokens":1}}`))
	}))
	defer server.Close()
	client := newAnthropicModel(ModelConfig{URL: server.URL, Model: "test", APIKey: "fake", ExtraBody: map[string]any{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "high", "custom_flag": true}}})
	for _, effort := range []string{"low", "max", "off", ""} {
		_, err := Call(t.Context(), client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("work")}}}, Config: &agentgo.CallConfig{ThinkingLevel: agentgo.NormalizeThinkingLevel(agentgo.ThinkingLevel(effort))}})
		if err != nil {
			t.Fatal(err)
		}
		body := <-bodies
		output := body["output_config"].(map[string]any)
		if output["custom_flag"] != true {
			t.Fatal("unrelated configuration lost")
		}
		if effort == "off" {
			if body["thinking"].(map[string]any)["type"] != "disabled" || output["effort"] != nil {
				t.Fatalf("body=%v", body)
			}
		} else {
			want := effort
			if want == "" {
				want = "high"
			}
			if output["effort"] != want || body["thinking"].(map[string]any)["type"] != "adaptive" {
				t.Fatalf("body=%v", body)
			}
		}
	}
}

func TestCompleteTextRejectsTruncatedAndReasoningOnlyResponses(t *testing.T) {
	visible := "partial plan"
	for _, reason := range []string{"length"} {
		response := &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.TextBlock(visible)}, StopReason: agentgo.StopReason(reason)}}

		if text, err := CompleteText(response); text != "" || !errors.Is(err, ErrOutputTruncated) {
			t.Fatalf("text=%q error=%v", text, err)
		}
	}
	response := &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.ThinkingBlock("private reasoning")}, StopReason: agentgo.StopReasonStop}}

	if _, err := CompleteText(response); err == nil {
		t.Fatal("reasoning used as final answer")
	}
	response.Message.Content = []agentgo.ContentBlock{agentgo.TextBlock(visible)}
	if text, err := CompleteText(response); err != nil || text != visible {
		t.Fatalf("text=%q error=%v", text, err)
	}
}
