package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/compforge/go-stdx/timeline"
	openai "github.com/openai/openai-go/v3"
	"github.com/qiankunli/case-code-review/internal/console"
)

// --- Multi-model router ---

// Tunables for LLMRouter. A router member that returns a fallover-worthy error is
// parked for routerCooldown so concurrent subtasks skip it instead of each re-hitting
// a model that's down/throttled. Router owns recovery across the pool, so member SDK
// retries are disabled instead of multiplying with fallover attempts.
const (
	routerDisableSDKRetries   = -1
	routerCooldown            = 30 * time.Second
	defaultRoutingCallTimeout = 180 * time.Second
)

// routing.policy values. priority: always prefer member 0, fall over on failure.
// round-robin: rotate the starting member each call to spread load across the pool
// (e.g. Ark rate-limits per endpoint, so spreading avoids saturating any single one).
const (
	policyPriority   = "priority"
	policyRoundRobin = "round-robin"
)

type routerMember struct {
	client LLMClient
	label  string // "protocol/model" for logs
	alias  string // routing alias, stamped onto the response so comments can be attributed
}

// LLMRouter is an LLMClient over an ordered pool of models. On a fallover-worthy
// failure (rate limit / 5xx / network) it advances to the next member; client-side
// errors (bad request / payload too large) short-circuit since another model would
// fail identically. Cooldown state is shared across concurrent CompletionsWithCtx
// calls (one ccr run's per-file subtasks), so a throttled model is skipped fleet-wide.
// Selection is governed by `policy` via the order() seam: "priority" (default) prefers
// member 0; "round-robin" spreads the starting member across the pool.
type LLMRouter struct {
	members     []routerMember
	policy      string
	callTimeout time.Duration
	mu          sync.Mutex
	cooldown    map[int]time.Time // member index → parked-until
	next        uint64            // round-robin cursor (guarded by mu)
}

// NewLLMRouter builds an LLMClient from an ordered pool under the given routing options.
// Empty policy/timeout use their defaults. A pool of one returns a plain client
// with unchanged single-model behavior and no router overhead.
func NewLLMRouter(eps []ResolvedEndpoint, routing RoutingOptions) LLMClient {
	if len(eps) == 1 {
		return NewLLMClient(eps[0])
	}
	policy := routing.Policy
	if policy == "" {
		policy = policyPriority
	}
	callTimeout := routing.CallTimeout
	if callTimeout <= 0 {
		callTimeout = defaultRoutingCallTimeout
	}
	members := make([]routerMember, len(eps))
	for i, ep := range eps {
		if ep.MaxRetries == 0 {
			ep.MaxRetries = routerDisableSDKRetries
		}
		members[i] = routerMember{client: NewLLMClient(ep), label: ep.Protocol + "/" + ep.Model, alias: ep.Alias}
	}
	return &LLMRouter{members: members, policy: policy, callTimeout: callTimeout, cooldown: make(map[int]time.Time)}
}

func (r *LLMRouter) CompletionsWithCtx(ctx context.Context, req ChatRequest) (response *ChatResponse, resultErr error) {
	ctx, finish := beginRequest(ctx)
	defer func() { finish(resultErr) }()
	parentCtx := ctx
	if r.callTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.callTimeout)
		defer cancel()
	}

	ctx, routingStage := beginStage(ctx, "llm.routing", append(remainingBudget(ctx), timeline.Field{Key: "budget_ms", Value: r.callTimeout.Milliseconds()})...)
	defer func() { endStage(ctx, routingStage, resultErr) }()

	var lastErr error
	order := r.order()
	for attemptIndex, i := range order {
		fields := append(remainingBudget(ctx), timeline.Field{Key: "endpoint", Value: r.members[i].alias}, timeline.Field{Key: "attempt", Value: attemptIndex + 1})
		attemptCtx, stage := beginStage(ctx, "llm.attempt", fields...)
		resp, err := r.members[i].client.CompletionsWithCtx(attemptCtx, req)
		endStage(attemptCtx, stage, err)
		if err == nil {
			if resp != nil {
				resp.Alias = r.members[i].alias // attribute downstream comments to this model
			}
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			if parentCtx.Err() != nil {
				return nil, parentCtx.Err()
			}
			return nil, routingTimeoutError(
				err, r.callTimeout, r.members[i], attemptIndex+1, len(order),
			)
		}
		if !shouldFallover(err) {
			return nil, err
		}
		r.park(i)
		fmt.Fprintf(console.Err(), "[llm-router] %s failed (%v) — trying next model\n", r.members[i].label, err)
	}
	return nil, fmt.Errorf("all %d models exhausted; last error: %w", len(r.members), lastErr)
}

// order returns member indices to try, non-parked first; parked ones are appended
// (not dropped) so an all-parked pool is still attempted as last resort. The live set
// is ordered per policy: priority keeps config order (member 0 preferred); round-robin
// rotates the start each call so load spreads across the pool.
func (r *LLMRouter) order() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	live := make([]int, 0, len(r.members))
	parked := make([]int, 0)
	for i := range r.members {
		if t, ok := r.cooldown[i]; ok {
			if now.Before(t) {
				parked = append(parked, i)
			} else {
				delete(r.cooldown, i)
				live = append(live, i)
			}
		} else {
			live = append(live, i)
		}
	}
	if r.policy == policyRoundRobin && len(live) > 1 {
		s := int(r.next % uint64(len(live)))
		r.next++
		rot := make([]int, 0, len(live))
		rot = append(rot, live[s:]...)
		rot = append(rot, live[:s]...)
		live = rot
	}
	return append(live, parked...)
}

func (r *LLMRouter) park(i int) {
	r.mu.Lock()
	r.cooldown[i] = time.Now().Add(routerCooldown)
	r.mu.Unlock()
}

// shouldFallover reports whether err warrants trying the next model. Availability
// failures (rate limit, server, network) → yes; a caller-cancelled context or a
// client-side request error (same payload fails on every model) → no.
func shouldFallover(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	var aerr *anthropic.Error
	if errors.As(err, &aerr) {
		return falloverStatus(aerr.StatusCode)
	}
	var oerr *openai.Error
	if errors.As(err, &oerr) {
		return falloverStatus(oerr.StatusCode)
	}
	return true // unknown (network blip / timeout / parse) → next model may succeed
}

func falloverStatus(code int) bool {
	switch code {
	case 400, 413, 422:
		return false // bad request / payload too large / unprocessable: deterministic across models
	default:
		return true // 401/403/404/408/409/429/5xx: a different provider/key/capacity may differ
	}
}
