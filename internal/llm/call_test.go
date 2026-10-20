package llm

import (
	"context"
	"errors"
	"testing"

	"github.com/compforge/agentgo"
)

// +case:id=canceled_completion_terminal,desc=`completion returns after its deadline`,expect=`the loop receives the original error and partial response rather than an empty stream`
func TestCompletionStreamRetainsCanceledTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	response := &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.TextBlock("partial")}, Usage: &agentgo.Usage{Output: 3}, StopReason: agentgo.StopReasonLength}}
	events, err := CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return response, context.DeadlineExceeded })
	if err != nil {
		t.Fatal(err)
	}
	event, ok := <-events
	if !ok || event.Type != agentgo.StreamEventError || !errors.Is(event.Err, context.DeadlineExceeded) || event.Message.TextContent() != "partial" || event.Message.Usage.Output != 3 || event.StopReason != agentgo.StopReasonLength {
		t.Fatalf("terminal event lost: %+v", event)
	}
	if _, ok := <-events; ok {
		t.Fatal("more than one terminal event")
	}
}

// +case:id=routed_attempt_budget,desc=`a failed attempt reports usage before a successful fallback`,expect=`all reported usage is charged once without charging the final response twice`
func TestBudgetChargesEachReportedAttemptOnce(t *testing.T) {
	client := NewBudgetModel(budgetTestClient(func(ctx context.Context, _ agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
		reportAttempt(ctx, agentgo.ModelAttempt{Index: 1, Usage: &agentgo.Usage{Input: 4, CacheRead: 2, CacheWrite: 3, Output: 1}}, errors.New("partial failure"))
		response := &agentgo.LLMResponse{Message: agentgo.Message{Usage: &agentgo.Usage{Input: 5, Output: 2}}}
		reportAttempt(ctx, agentgo.ModelAttempt{Index: 2, Usage: response.Message.Usage}, nil)
		return response, nil
	}), 15, nil)
	observed := 0
	ctx := ObserveAttempts(t.Context(), func(context.Context, agentgo.ModelAttempt, error) { observed++ })
	if _, err := client.Generate(ctx, nil, nil); err != nil {
		t.Fatal(err)
	}
	if client.Used() != 15 || observed != 2 {
		t.Fatalf("used=%d observed=%d", client.Used(), observed)
	}
	if _, err := client.Generate(ctx, nil, nil); !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("budget error=%v", err)
	}
}

func TestBudgetDoesNotInventMissingAttemptUsage(t *testing.T) {
	client := NewBudgetModel(budgetTestClient(func(ctx context.Context, _ agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
		reportAttempt(ctx, agentgo.ModelAttempt{Index: 1}, context.DeadlineExceeded)
		return nil, context.DeadlineExceeded
	}), 20, nil)
	_, err := client.Generate(t.Context(), nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || client.Used() != 0 {
		t.Fatalf("used=%d error=%v", client.Used(), err)
	}
}
