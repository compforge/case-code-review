package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/compforge/agentgo"
)

func TestNativeStreamPublishesProgressBeforeCompletion(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request["stream"] != true {
			t.Error("provider request is not streaming")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ready\"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, "data: {\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	defer unblock()
	budget := NewBudgetModel(NewModel(ResolvedEndpoint{URL: server.URL, Model: "test"}), 10, nil)
	progress := false
	executor := agentgo.NewModelExecutor(agentgo.ModelExecutorConfig{Middlewares: []agentgo.ModelMiddleware{ModelRequestMiddleware}, OnEvent: func(event agentgo.Event) {
		if event.Type == agentgo.EventModelStream && event.ModelStream.Type == agentgo.StreamEventTextDelta {
			progress = true
			unblock()
		}
	}})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	result, err := executor.Call(ctx, budget, agentgo.ModelExecution{Execution: agentgo.Execution{ID: "stream", Kind: agentgo.ExecutionKindModel, Attempt: 1}, Request: agentgo.LLMRequest{Messages: []agentgo.Message{agentgo.UserMsg("test")}}})
	if err != nil || !progress || result.Message.TextContent() != "ready" || budget.Used() != 5 {
		t.Fatalf("result=%+v progress=%v budget=%d err=%v", result, progress, budget.Used(), err)
	}
}

type failedStreamModel struct{ report bool }

func (failedStreamModel) SupportsTools() bool { return true }
func (failedStreamModel) Generate(context.Context, []agentgo.Message, []agentgo.ToolSpec, ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	panic("unexpected non-streaming call")
}
func (m failedStreamModel) GenerateStream(ctx context.Context, _ []agentgo.Message, _ []agentgo.ToolSpec, _ ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	usage := &agentgo.Usage{Input: 3, Output: 7, Reasoning: 5}
	if m.report {
		reportAttempt(ctx, agentgo.ModelAttempt{Index: 1, Usage: usage}, errors.New("failed"))
	}
	events := make(chan agentgo.StreamEvent, 1)
	events <- agentgo.StreamEvent{Type: agentgo.StreamEventError, Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.ThinkingBlock("partial")}, Usage: usage}, Err: errors.New("failed")}
	close(events)
	return events, nil
}

func TestStreamingBudgetChargesFailedUsageOnce(t *testing.T) {
	for _, report := range []bool{false, true} {
		t.Run(fmt.Sprint(report), func(t *testing.T) {
			budget := NewBudgetModel(failedStreamModel{report: report}, 10, nil)
			response, err := Call(t.Context(), budget, agentgo.LLMRequest{})
			if err == nil || response.Message.Usage == nil || response.Message.Usage.Reasoning != 5 || budget.Used() != 10 {
				t.Fatalf("response=%+v used=%d err=%v", response, budget.Used(), err)
			}
			if _, err = Call(t.Context(), budget, agentgo.LLMRequest{}); !errors.Is(err, ErrTokenBudget) {
				t.Fatalf("budget admission=%v", err)
			}
		})
	}
}
