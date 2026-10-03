package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

type budgetTestClient func(context.Context, ChatRequest) (*ChatResponse, error)

func (f budgetTestClient) CompletionsWithCtx(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return f(ctx, req)
}

func TestTokenBudgetStopsAtBoundaryWithoutCountingCacheTwice(t *testing.T) {
	calls := 0
	notices := 0
	client := NewBudgetClient(budgetTestClient(func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls++
		return &ChatResponse{Usage: &UsageInfo{PromptTokens: 8, CompletionTokens: 2, CacheReadTokens: 4, CacheWriteTokens: 2, TotalTokens: 10}}, nil
	}), 10, func(used, limit int64) {
		notices++
		if used != 10 || limit != 10 {
			t.Errorf("notice=%d/%d", used, limit)
		}
	})
	if _, err := client.CompletionsWithCtx(t.Context(), ChatRequest{}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := client.CompletionsWithCtx(t.Context(), ChatRequest{}); !errors.Is(err, ErrTokenBudget) {
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
	client := NewBudgetClient(budgetTestClient(func(context.Context, ChatRequest) (*ChatResponse, error) {
		return &ChatResponse{Usage: &UsageInfo{TotalTokens: 7}}, problem
	}), 0, nil)
	for range 3 {
		if _, err := client.CompletionsWithCtx(t.Context(), ChatRequest{}); err != problem {
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
	client := NewBudgetClient(budgetTestClient(func(context.Context, ChatRequest) (*ChatResponse, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return &ChatResponse{Usage: &UsageInfo{PromptTokens: 10}}, nil
	}), 10, func(int64, int64) { notices.Add(1) })
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := client.CompletionsWithCtx(t.Context(), ChatRequest{}); err != nil {
				t.Error(err)
			}
		}()
	}
	for range workers {
		<-started
	}
	close(release)
	wg.Wait()
	if _, err := client.CompletionsWithCtx(t.Context(), ChatRequest{}); !errors.Is(err, ErrTokenBudget) {
		t.Fatalf("error=%v", err)
	}
	if calls.Load() != workers || client.Used() != 40 || notices.Load() != 1 {
		t.Fatalf("calls=%d used=%d notices=%d", calls.Load(), client.Used(), notices.Load())
	}
}
