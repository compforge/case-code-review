package harness

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
)

const wrapUpTimeReserve = 90 * time.Second
const wrapUpTurnReserve = 2

// Reasoning and structured result arguments both consume output. A recent short
// reply must not shrink the completion allowance to almost zero.
const wrapUpOutputFloor = 4096

type tokenAllowance interface {
	Remaining() (tokens int64, limited bool)
}

// turnController owns per-turn injections and convergence state. ContextManager
// only projects and compacts messages; business-neutral turn timing belongs to
// the Execution lifecycle exposed by AgentGo's BeforeTurn hook.
type turnController struct {
	maxTurns     int
	wrapUpAfter  int
	wrapUpAt     time.Time
	currentTurn  int
	wrapUpPrompt string
	scope        session.Scope
	provider     TurnContextProvider
	budget       tokenAllowance
	history      *session.SessionHistory
	maxOutput    int
	toolTokens   int
	outputs      [3]int64
	outputIndex  int

	mu            sync.Mutex
	wrapUpIssued  bool
	wrapUpPending bool
}

func newTurnController(spec ExecutionSpec) *turnController {
	budget, _ := spec.LLMClient.(tokenAllowance)
	schemas, _ := json.Marshal(spec.ToolDefs)
	return &turnController{
		budget:       budget,
		history:      spec.Session,
		maxOutput:    spec.MaxTokens,
		toolTokens:   llm.CountTokens(string(schemas)),
		maxTurns:     spec.MaxTurns,
		wrapUpAfter:  spec.WrapUpAfterTurns,
		wrapUpAt:     spec.WrapUpAt,
		wrapUpPrompt: spec.WrapUpPrompt,
		scope:        spec.Scope,
		provider:     spec.TurnContext,
	}
}

func (c *turnController) BeforeTurn(
	ctx context.Context,
	turn agentgo.BeforeTurnContext,
) ([]agentgo.AgentMessage, error) {
	c.mu.Lock()
	c.currentTurn = turn.TurnIndex
	c.mu.Unlock()
	var messages []agentgo.AgentMessage
	if c.provider != nil {
		messages = append(messages, c.provider.PullTurnContext(ctx, c.scope)...)
	}
	contextMessages := append(append([]agentgo.AgentMessage(nil), turn.Context.Messages...), messages...)
	c.shouldWrapUp(ctx, turn.TurnIndex, contextMessages)
	if c.takeReminder() {
		messages = append(messages, msg.Text("user", c.wrapUpPrompt))
	}
	return rawMessages(messages), nil
}

func (c *turnController) WrapUpIssued() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wrapUpIssued
}

func (c *turnController) observeUsage(usage *llm.UsageInfo) {
	if usage == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.outputs[c.outputIndex%len(c.outputs)] = usage.CompletionTokens
	c.outputIndex++
}

func (c *turnController) shouldWrapUp(ctx context.Context, turnIndex int, messages []agentgo.AgentMessage) bool {
	if c.wrapUpPrompt == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wrapUpIssued {
		return false
	}

	plannedWrapUp := c.wrapUpAfter > 0 && turnIndex > c.wrapUpAfter
	nearTurnLimit := c.maxTurns > 0 && c.maxTurns-turnIndex+1 <= wrapUpTurnReserve
	timeLeft := time.Duration(0)
	nearDeadline := false
	if deadline, ok := ctx.Deadline(); ok {
		timeLeft = time.Until(deadline)
		nearDeadline = timeLeft < wrapUpTimeReserve
	}
	var reasons []string
	if plannedWrapUp {
		reasons = append(reasons, "investigation_turns")
	}
	if nearTurnLimit {
		reasons = append(reasons, "turn_limit")
	}
	if !c.wrapUpAt.IsZero() && !time.Now().Before(c.wrapUpAt) {
		reasons = append(reasons, "investigation_time")
	}
	if nearDeadline {
		reasons = append(reasons, "deadline")
	}
	var remaining, investigation, wrapUp int64
	var limited bool
	if c.budget != nil {
		remaining, limited = c.budget.Remaining()
		if limited {
			investigation, wrapUp = c.forecast(messages)
			if remaining <= investigation+wrapUp {
				reasons = append(reasons, "token_budget")
			}
		}
	}
	if len(reasons) > 0 {
		c.wrapUpIssued = true
		c.wrapUpPending = true
		if c.history != nil {
			c.history.WriteArtifactContext(ctx, "wrap_up", map[string]any{
				"scope_id": c.scope.ID, "turn": turnIndex, "reasons": reasons,
				"remaining_time_ms": timeLeft.Milliseconds(), "token_limited": limited,
				"investigation_deadline": c.wrapUpAt,
				"remaining_tokens":       remaining, "investigation_tokens": investigation, "wrap_up_tokens": wrapUp,
			})
		}
		return true
	}
	return false
}

// forecast runs before this turn's compaction. The existing estimator anchors on
// reported input usage and counts subsequent messages; after a committed compact
// it estimates the new baseline instead. Per-tool view limits avoid counting raw
// evidence that cannot reach the model. Cross-message dedup can reduce it further,
// so this is a conservative forecast, not a reservation or a billing ceiling.
func (c *turnController) forecast(messages []agentgo.AgentMessage) (investigation, wrapUp int64) {
	estimate := agentcontext.EstimateContextTokens(msg.LimitToolMessages(messages))
	input := int64(estimate.Tokens)
	if estimate.UsageTokens == 0 {
		input += int64(c.toolTokens)
	}
	output := int64(wrapUpOutputFloor)
	for _, tokens := range c.outputs {
		output = max(output, tokens)
	}
	if c.maxOutput > 0 {
		output = min(output, int64(c.maxOutput))
	}
	investigation = input + output
	// The next assistant answer itself becomes input to the final request. Future
	// tool output is unknown; shared concurrent calls and summary calls can also
	// spend the remaining allowance, which the client still checks at admission.
	wrapUp = input + 2*output + int64(llm.CountTokens(c.wrapUpPrompt))
	return investigation, wrapUp
}

// checkTime closes tool admission when an in-flight model/tool call crosses the
// exploration boundary. The next model turn receives the ordinary wrap-up prompt.
func (c *turnController) checkTime(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.wrapUpIssued || c.wrapUpPrompt == "" || c.wrapUpAt.IsZero() || time.Now().Before(c.wrapUpAt) {
		return
	}
	c.wrapUpIssued, c.wrapUpPending = true, true
	if c.history != nil {
		c.history.WriteArtifactContext(ctx, "wrap_up", map[string]any{
			"scope_id": c.scope.ID, "turn": c.currentTurn, "reasons": []string{"investigation_time"},
			"investigation_deadline": c.wrapUpAt,
		})
	}
}

func (c *turnController) takeReminder() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	pending := c.wrapUpPending
	c.wrapUpPending = false
	return pending
}
