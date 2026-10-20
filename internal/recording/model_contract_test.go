package recording

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
)

type nativeRecordingModel struct {
	t        *testing.T
	request  agentgo.LLMRequest
	response *agentgo.LLMResponse
	err      error
}

func (m nativeRecordingModel) Generate(_ context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	config := agentgo.ResolveCallConfig(opts)
	if !reflect.DeepEqual(messages, m.request.Messages) || !reflect.DeepEqual(tools, m.request.Tools) || !reflect.DeepEqual(&config, m.request.Config) {
		m.t.Fatal("recording changed native call")
	}
	return m.response, m.err
}
func (m nativeRecordingModel) SupportsTools() bool { return true }
func (m nativeRecordingModel) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return llm.CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return m.Generate(ctx, messages, tools, opts...) })
}

func readAuxiliaryEvents(t *testing.T, review *Review) []agentsession.EventData {
	t.Helper()
	path, err := review.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + ".sessions/*.jsonl")
	if err != nil || len(paths) != 1 {
		t.Fatalf("session paths=%v err=%v", paths, err)
	}
	file, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []agentsession.EventData
	_, err = agentsession.Read(file, func(record agentsession.Record) error {
		if record.Type != string(agentgo.EventModelRequest) && record.Type != string(agentgo.EventModelExecEnd) && record.Type != string(agentgo.EventModelAttempt) {
			return nil
		}
		var event agentsession.EventData
		if err := json.Unmarshal(record.Data, &event); err != nil {
			return err
		}
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

// +case:id=auxiliary_native_session,desc=`an auxiliary call carries provider state and full call configuration`,expect=`the exact native request and response survive recording, including failed partial responses`
func TestAuxiliarySessionPreservesNativeContract(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint(failed), func(t *testing.T) {
			review := New(t.TempDir(), "test", "model", SessionOptions{})
			defer review.Finalize()
			scope := review.GetOrCreateScope(Scope{ID: "plan", Kind: "unit"})
			state := &agentgo.ProviderState{Provider: "test", Model: "model", Data: json.RawMessage(`{"signature":"opaque"}`)}
			thinking := agentgo.ThinkingBlock("reasoning")
			thinking.ProviderState = state
			response := &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{thinking, agentgo.TextBlock("partial answer")}, StopReason: agentgo.StopReasonLength, Usage: &agentgo.Usage{Input: 3, Output: 7, Reasoning: 5}, Metadata: map[string]any{"route_endpoint": "primary"}}}
			temperature := 0.2
			request := agentgo.LLMRequest{Messages: []agentgo.Message{response.Message, agentgo.UserMsg("continue")}, Tools: []agentgo.ToolSpec{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}, Config: &agentgo.CallConfig{Temperature: &temperature, ThinkingLevel: "xhigh", ThinkingBudget: 128, MaxTokens: 256, SessionID: "session", PromptCacheKey: "prefix", ToolChoice: map[string]any{"name": "inspect"}, ResponseFormat: &agentgo.ResponseFormat{Type: agentgo.ResponseFormatJSONObject}}}
			model := nativeRecordingModel{t: t, request: request, response: response}
			if failed {
				model.err = errors.New("partial provider failure")
			}
			got, err := llm.Call(t.Context(), scope.RecordingModel(model, PlanTask), request)
			if !reflect.DeepEqual(got.Message.Content, response.Message.Content) || !reflect.DeepEqual(got.Message.Usage, response.Message.Usage) || err != model.err {
				t.Fatalf("response=%+v err=%v", got, err)
			}
			events := readAuxiliaryEvents(t, review)
			codec, err := msg.Codec()
			if err != nil {
				t.Fatal(err)
			}
			requests, responses := 0, 0
			for _, event := range events {
				if event.ModelRequest != nil {
					requests++
					gotJSON, _ := json.Marshal(event.ModelRequest)
					wantJSON, _ := json.Marshal(request)
					if string(gotJSON) != string(wantJSON) {
						t.Fatalf("request changed in Session: %+v", event.ModelRequest)
					}
				}
				body := event.Message
				if failed {
					body = event.PartialMessage
				}
				if len(body) > 0 {
					responses++
					var message agentgo.Message
					if err := codec.Unmarshal(body, &message); err != nil {
						t.Fatal(err)
					}
					message.Timestamp = response.Message.Timestamp
					if !reflect.DeepEqual(message, response.Message) {
						t.Fatalf("response changed in Session: %+v", message)
					}
					if failed && event.Error == "" {
						t.Fatal("partial failure lost")
					}
				}
			}
			if requests != 1 || responses != 1 {
				t.Fatalf("requests=%d responses=%d", requests, responses)
			}
			review.Finalize()
			path, err := review.TranscriptPath()
			if err != nil {
				t.Fatal(err)
			}
			transcript, err := ReadReview(path)
			if err != nil {
				t.Fatal(err)
			}
			cost := transcript.Costs().Total
			wantErrors := 0
			if failed {
				wantErrors = 1
			}
			if cost.Calls != 1 || cost.Errors != wantErrors || cost.UnknownUsage != 0 || cost.Tokens.CompletionTokens != 7 || cost.Tokens.ReasoningTokens != 5 {
				t.Fatalf("cost=%+v", cost)
			}

		})
	}
}

func TestAuxiliarySessionRecordsRouterAttempts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"unavailable"}}`, 503)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer second.Close()
	model := llm.NewRouter([]llm.ResolvedEndpoint{{URL: first.URL, Model: "test", Alias: "primary"}, {URL: second.URL, Model: "test", Alias: "fallback"}}, llm.RoutingOptions{})
	review := New(t.TempDir(), "test", "test", SessionOptions{})
	defer review.Finalize()
	scope := review.GetOrCreateScope(Scope{ID: "plan", Kind: "unit"})
	_, err := llm.Call(t.Context(), scope.RecordingModel(model, PlanTask), agentgo.LLMRequest{Messages: []agentgo.Message{agentgo.UserMsg("review")}})
	if err != nil {
		t.Fatal(err)
	}
	var attempts []agentgo.ModelAttempt
	for _, event := range readAuxiliaryEvents(t, review) {
		if event.ModelAttempt != nil {
			attempts = append(attempts, *event.ModelAttempt)
		}
	}
	if len(attempts) != 2 || attempts[0].Endpoint != "primary" || !attempts[0].WillFailover || attempts[1].Endpoint != "fallback" || attempts[1].Usage == nil || attempts[1].Usage.Output != 2 {
		t.Fatalf("attempts=%+v", attempts)
	}
}
