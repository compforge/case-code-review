package harness

import (
	"context"
	"sync"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
)

const wrapUpTimeReserve = 90 * time.Second
const wrapUpTurnReserve = 2

// turnController owns per-turn injections and convergence state. ContextManager
// only projects and compacts messages; business-neutral turn timing belongs to
// the Execution lifecycle exposed by AgentGo's BeforeTurn hook.
type turnController struct {
	maxTurns     int
	wrapUpPrompt string
	scope        session.Scope
	provider     TurnContextProvider

	mu           sync.Mutex
	wrapUpIssued bool
}

func newTurnController(spec ExecutionSpec) *turnController {
	return &turnController{
		maxTurns:     spec.MaxTurns,
		wrapUpPrompt: spec.WrapUpPrompt,
		scope:        spec.Scope,
		provider:     spec.TurnContext,
	}
}

func (c *turnController) BeforeTurn(
	ctx context.Context,
	turn agentgo.BeforeTurnContext,
) ([]agentgo.AgentMessage, error) {
	var messages []msg.Msg
	if c.provider != nil {
		messages = append(messages, c.provider.PullTurnContext(ctx, c.scope)...)
	}
	if c.shouldWrapUp(ctx, turn.TurnIndex) {
		messages = append(messages, msg.Text("user", c.wrapUpPrompt))
	}
	return wrapDomainMessages(msg.CloneAll(messages)), nil
}

func (c *turnController) WrapUpIssued() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wrapUpIssued
}

func (c *turnController) shouldWrapUp(ctx context.Context, turnIndex int) bool {
	if c.wrapUpPrompt == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wrapUpIssued {
		return false
	}

	nearTurnLimit := c.maxTurns > 0 && c.maxTurns-turnIndex+1 <= wrapUpTurnReserve
	nearDeadline := false
	if deadline, ok := ctx.Deadline(); ok {
		nearDeadline = time.Until(deadline) < wrapUpTimeReserve
	}
	if nearTurnLimit || nearDeadline {
		c.wrapUpIssued = true
		return true
	}
	return false
}
