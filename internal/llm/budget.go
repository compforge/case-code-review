package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/compforge/agentgo"
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

// BudgetModel shares one soft allowance across all calls in a run, including
// compression and auxiliary tasks. Requests already admitted (and provider
// retries inside them) can finish; missing provider usage is not fabricated.
// It does not reserve estimated cost or promise an exact billing ceiling.
type BudgetModel struct {
	client      agentgo.ChatModel
	limit       int64
	used        atomic.Int64
	once        sync.Once
	onExhausted func(used, limit int64)
}

func NewBudgetModel(client agentgo.ChatModel, limit int64, onExhausted func(int64, int64)) *BudgetModel {
	return &BudgetModel{client: client, limit: limit, onExhausted: onExhausted}
}
func (c *BudgetModel) Used() int64 { return c.used.Load() }

// Remaining reports the shared, reported allowance. It is a snapshot, not a
// reservation: in-flight calls may still spend tokens after it is read.
func (c *BudgetModel) Remaining() (tokens int64, limited bool) {
	return max(0, c.limit-c.Used()), c.limit > 0
}

func (c *BudgetModel) Check() error {
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

// +spec=`Each routed attempt's reported usage is charged once; the final response is only a fallback when no attempt facts exist`
func (c *BudgetModel) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	var attempted atomic.Bool
	ctx = ObserveAttempts(ctx, func(_ context.Context, attempt agentgo.ModelAttempt, _ error) {
		attempted.Store(true)
		c.addUsage(attempt.Usage)
	})
	response, err := c.client.Generate(ctx, messages, tools, opts...)
	if !attempted.Load() && response != nil {
		c.addUsage(response.Message.Usage)
	}
	return response, err
}
func (c *BudgetModel) addUsage(usage *agentgo.Usage) {
	if usage == nil {
		return
	}
	tokens := usage.Input + usage.CacheWrite + usage.Output
	if tokens == 0 {
		tokens = usage.TotalTokens
	}
	c.used.Add(int64(tokens))
	_ = c.Check()
}

func (c *BudgetModel) SupportsTools() bool { return c.client.SupportsTools() }
func (c *BudgetModel) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	if err := c.Check(); err != nil {
		return nil, err
	}
	var attempted atomic.Bool
	ctx = ObserveAttempts(ctx, func(_ context.Context, attempt agentgo.ModelAttempt, _ error) {
		attempted.Store(true)
		c.addUsage(attempt.Usage)
	})
	stream, err := c.client.GenerateStream(ctx, messages, tools, opts...)
	if err != nil {
		return nil, err
	}
	return observeStream(ctx, stream, func(message agentgo.Message, err error, sourceEnded bool) (agentgo.Message, error) {
		if sourceEnded && !attempted.Load() {
			c.addUsage(message.Usage)
		}
		return message, err
	}), nil
}
