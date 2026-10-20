package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/compforge/agentgo"
)

type budgetTestClient func(context.Context, agentgo.LLMRequest) (*agentgo.LLMResponse, error)

func (f budgetTestClient) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	config := agentgo.ResolveCallConfig(opts)
	req := agentgo.LLMRequest{Messages: messages, Tools: tools,
		Config: &config}

	return f(ctx, req)
}

func TestTokenBudgetStopsAtBoundaryWithoutCountingCacheTwice(t *testing.T) {
	calls := 0
	notices := 0
	client := NewBudgetModel(budgetTestClient(func(context.Context, agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
		calls++
		return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{}, StopReason: agentgo.StopReasonStop, Usage: &agentgo.Usage{Input: 8 - 2, Output: 2, CacheRead: 4, CacheWrite: 2, TotalTokens: 10}}}, nil
	}), 10, func(used, limit int64) {
		notices++
		if used != 10 || limit != 10 {
			t.Errorf("notice=%d/%d", used, limit)
		}
	})
	if _, err := Call(t.Context(), client, agentgo.LLMRequest{Config: &agentgo.CallConfig{}}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := Call(t.Context(), client, agentgo.LLMRequest{Config: &agentgo.CallConfig{}}); !errors.Is(err, ErrTokenBudget) {
			t.Fatalf("error=%v", err)
		}
	}
	if calls != 1 || notices != 1 || client.Used() != 10 {
		t.Fatalf("calls=%d notices=%d used=%d", calls, notices, client.Used())
	}
	details := DescribeError(client.Check())
	if details == nil || details.Code != "token_budget_exhausted" {
		t.Fatalf("details=%+v", details)
	}
}

func TestBudgetAccountsReportedUsageEvenOnErrorAndUnlimitedRuns(t *testing.T) {
	problem := errors.New("partial response")
	client := NewBudgetModel(budgetTestClient(func(context.Context, agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
		return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{}, StopReason: agentgo.StopReasonStop, Usage: &agentgo.Usage{TotalTokens: 7}}}, problem
	}), 0, nil)
	for range 3 {
		if _, err := Call(t.Context(), client, agentgo.LLMRequest{Config: &agentgo.CallConfig{}}); err != problem {
			t.Fatal(err)
		}
	}
	if client.Used() != 21 || client.Check() != nil {
		t.Fatalf("unlimited used=%d", client.Used())
	}
}

func TestBudgetAllowsAdmittedConcurrentRequestsButNoLaterCalls(t *testing.T) {
	const workers = 4
	started := make(chan struct{}, workers)
	release := make(chan struct{})
	var calls, notices atomic.Int64
	client := NewBudgetModel(budgetTestClient(func(context.Context, agentgo.LLMRequest) (*agentgo.LLMResponse, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return &agentgo.LLMResponse{Message: agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{}, StopReason: agentgo.StopReasonStop, Usage: &agentgo.Usage{Input: 10}}}, nil
	}), 10, func(int64, int64) { notices.Add(1) })
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Call(t.Context(), client, agentgo.LLMRequest{Config: &agentgo.CallConfig{}}); err != nil {
				t.Error(err)
			}
		}()
	}
	for range workers {
		<-started
	}
	close(release)
	wg.Wait()
	if _, err := Call(t.Context(), client, agentgo.LLMRequest{Config: &agentgo.CallConfig{}}); !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != workers || client.Used() != 40 || notices.Load() != 1 {
		t.Fatalf("calls=%d used=%d notices=%d", calls.Load(), client.Used(), notices.Load())
	}
}

func (f budgetTestClient) SupportsTools() bool { return true }
func (f budgetTestClient) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return f.Generate(ctx, messages, tools, opts...) })
}
