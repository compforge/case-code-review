package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ErrTokenBudget means a run's reported token allowance is exhausted. It is a
// local admission decision, not a provider failure and must not be retried.
var ErrTokenBudget = errors.New("token budget exhausted")

type TokenBudgetError struct{ Used, Limit int64 }

func (e *TokenBudgetError) Error() string {
	return fmt.Sprintf("%s: used %d, limit %d", ErrTokenBudget, e.Used, e.Limit)
}
func (e *TokenBudgetError) Unwrap() error   { return ErrTokenBudget }
func (e *TokenBudgetError) Retryable() bool { return false }
func (e *TokenBudgetError) errorDetails() ErrorDetails {
	return ErrorDetails{Kind: "policy", Phase: "admission", ErrorType: "budget", Code: "token_budget_exhausted", Message: e.Error(), Attributes: map[string]any{"used": e.Used, "limit": e.Limit}}
}

// BudgetClient shares one soft allowance across all calls in a run, including
// compression and auxiliary tasks. Requests already admitted (and provider
// retries inside them) can finish; missing provider usage is not fabricated.
// It does not reserve estimated cost or promise an exact billing ceiling.
type BudgetClient struct {
	client      LLMClient
	limit       int64
	used        atomic.Int64
	once        sync.Once
	onExhausted func(used, limit int64)
}

func NewBudgetClient(client LLMClient, limit int64, onExhausted func(int64, int64)) *BudgetClient {
	return &BudgetClient{client: client, limit: limit, onExhausted: onExhausted}
}
func (c *BudgetClient) Used() int64 { return c.used.Load() }
func (c *BudgetClient) Check() error {
	used := c.Used()
	if c.limit <= 0 || used < c.limit {
		return nil
	}
	c.once.Do(func() {
		if c.onExhausted != nil {
			c.onExhausted(used, c.limit)
		}
	})
	return &TokenBudgetError{Used: used, Limit: c.limit}
}
func (c *BudgetClient) CompletionsWithCtx(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	response, err := c.client.CompletionsWithCtx(ctx, req)
	if response != nil && response.Usage != nil {
		usage := response.Usage
		// CCR adapters normalize PromptTokens to include cached input. TotalTokens
		// is a fallback for clients reporting only an aggregate; never add it twice.
		tokens := usage.PromptTokens + usage.CompletionTokens
		if tokens == 0 {
			tokens = usage.TotalTokens
		}
		c.used.Add(tokens)
		_ = c.Check()
	}
	return response, err
}
