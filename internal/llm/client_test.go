package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/compforge/agentgo"
)

func TestNewOpenAIClient_URLNormalization(t *testing.T) {
	tests := []struct {
		name     string
		inputURL string
		wantURL  string
	}{
		{
			name:     "base URL without trailing slash",
			inputURL: "https://api.example.com/v1",
			wantURL:  "https://api.example.com/v1/chat/completions",
		},
		{
			name:     "base URL with trailing slash",
			inputURL: "https://api.example.com/v1/",
			wantURL:  "https://api.example.com/v1/chat/completions",
		},
		{
			name:     "full URL already has chat/completions",
			inputURL: "https://api.example.com/v1/chat/completions",
			wantURL:  "https://api.example.com/v1/chat/completions",
		},
		{
			name:     "full URL with trailing slash",
			inputURL: "https://api.example.com/v1/chat/completions/",
			wantURL:  "https://api.example.com/v1/chat/completions",
		},
		{
			name:     "bare host",
			inputURL: "https://api.example.com",
			wantURL:  "https://api.example.com/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newOpenAIModel(ModelConfig{URL: tt.inputURL})
			if client.cfg.URL != tt.wantURL {
				t.Errorf("got URL %q, want %q", client.cfg.URL, tt.wantURL)
			}
		})
	}
}

func TestNewAnthropicClient_URLNormalization(t *testing.T) {
	tests := []struct {
		name     string
		inputURL string
		wantURL  string
	}{
		{
			name:     "bare host",
			inputURL: "https://api.anthropic.com",
			wantURL:  "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "bare host with trailing slash",
			inputURL: "https://api.anthropic.com/",
			wantURL:  "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "full URL already has /v1/messages",
			inputURL: "https://api.anthropic.com/v1/messages",
			wantURL:  "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "full URL with trailing slash",
			inputURL: "https://api.anthropic.com/v1/messages/",
			wantURL:  "https://api.anthropic.com/v1/messages",
		},
		{
			name:     "custom proxy base URL",
			inputURL: "https://proxy.example.com/anthropic",
			wantURL:  "https://proxy.example.com/anthropic/v1/messages",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newAnthropicModel(ModelConfig{URL: tt.inputURL})
			if client.cfg.URL != tt.wantURL {
				t.Errorf("got URL %q, want %q", client.cfg.URL, tt.wantURL)
			}
		})
	}
}

func TestAnthropicClient_UsesConfiguredXAPIKeyHeader(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "env-oauth-token")

	var gotXAPIKey string
	var gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXAPIKey = r.Header.Get("X-Api-Key")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeModelStream(t, w, `{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"model":"claude-test",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer server.Close()

	client := newAnthropicModel(ModelConfig{
		URL:        server.URL + "/v1/messages",
		APIKey:     "sk-ant-api03-test",
		Model:      "claude-test",
		AuthHeader: "x-api-key",
	})

	_, err := Call(context.Background(), client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("ping")}}}, Config: &agentgo.CallConfig{MaxTokens: 64}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotXAPIKey != "sk-ant-api03-test" {
		t.Errorf("X-Api-Key = %q, want %q", gotXAPIKey, "sk-ant-api03-test")
	}
	if gotAuthorization != "" {
		t.Errorf("Authorization = %q, want empty", gotAuthorization)
	}
}

func TestAnthropicClient_UsesConfiguredAuthorizationHeader(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-api-key")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	var gotXAPIKey string
	var gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXAPIKey = r.Header.Get("X-Api-Key")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeModelStream(t, w, `{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"model":"claude-test",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer server.Close()

	client := newAnthropicModel(ModelConfig{
		URL:        server.URL + "/v1/messages",
		APIKey:     "oauth-token",
		Model:      "claude-test",
		AuthHeader: "authorization",
	})

	_, err := Call(context.Background(), client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("ping")}}}, Config: &agentgo.CallConfig{MaxTokens: 64}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Errorf("Authorization = %q, want %q", gotAuthorization, "Bearer oauth-token")
	}
	if gotXAPIKey != "" {
		t.Errorf("X-Api-Key = %q, want empty", gotXAPIKey)
	}
}

func TestAnthropicClient_DefaultsToAuthorizationHeader(t *testing.T) {
	var gotXAPIKey string
	var gotAuthorization string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXAPIKey = r.Header.Get("X-Api-Key")
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		writeModelStream(t, w, `{
			"id":"msg_test",
			"type":"message",
			"role":"assistant",
			"model":"claude-test",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":1,"output_tokens":1}
		}`)
	}))
	defer server.Close()

	client := newAnthropicModel(ModelConfig{
		URL:    server.URL + "/v1/messages",
		APIKey: "oauth-token",
		Model:  "claude-test",
	})

	_, err := Call(context.Background(), client, agentgo.LLMRequest{Messages: []agentgo.Message{{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("ping")}}}, Config: &agentgo.CallConfig{MaxTokens: 64}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Errorf("Authorization = %q, want %q", gotAuthorization, "Bearer oauth-token")
	}
	if gotXAPIKey != "" {
		t.Errorf("X-Api-Key = %q, want empty", gotXAPIKey)
	}
}
