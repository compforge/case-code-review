package llm

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
	client := NewOpenAIClient(ClientConfig{URL: server.URL, APIKey: "fake", ExtraBody: defaults, MaxRetries: -1})
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh", "max", "off", "none", "auto", ""} {
		response, err := client.CompletionsWithCtx(t.Context(), ChatRequest{Model: "test", Messages: []Message{NewTextMessage("user", "work")}, ReasoningEffort: effort})
		if err != nil {
			t.Fatal(err)
		}
		body := <-bodies
		want, mode := effort, "enabled"
		switch effort {
		case "off", "none":
			want, mode = "none", "disabled"
		case "auto", "":
			want = "high"
		}
		if body["reasoning_effort"] != want || body["thinking"].(map[string]any)["type"] != mode || body["custom_flag"] != true {
			t.Fatalf("effort=%q body=%v", effort, body)
		}
		if response.Usage.ReasoningTokens != 32 || response.Usage.TotalTokens != 36 || response.Choices[0].FinishReason != "length" {
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
	client := NewAnthropicClient(ClientConfig{URL: server.URL, APIKey: "fake", ExtraBody: map[string]any{"thinking": map[string]any{"type": "adaptive"}, "output_config": map[string]any{"effort": "high", "custom_flag": true}}, MaxRetries: -1})
	for _, effort := range []string{"low", "max", "off", ""} {
		_, err := client.CompletionsWithCtx(t.Context(), ChatRequest{Model: "test", Messages: []Message{NewTextMessage("user", "work")}, ReasoningEffort: effort})
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
	for _, reason := range []string{"length", "max_tokens"} {
		response := &ChatResponse{Choices: []Choice{{FinishReason: reason, Message: ResponseMessage{Content: &visible}}}}
		if text, err := response.CompleteText(); text != "" || !errors.Is(err, ErrOutputTruncated) {
			t.Fatalf("text=%q error=%v", text, err)
		}
	}
	response := &ChatResponse{Choices: []Choice{{FinishReason: "stop", Message: ResponseMessage{ReasoningContent: "private reasoning"}}}}
	if _, err := response.CompleteText(); err == nil {
		t.Fatal("reasoning used as final answer")
	}
	response.Choices[0].Message.Content = &visible
	if text, err := response.CompleteText(); err != nil || text != visible {
		t.Fatalf("text=%q error=%v", text, err)
	}
}
