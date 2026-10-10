package runner

import (
	"context"
	"sync"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
)

// unitBudget belongs to one Unit across discovery, queued verification and Trial.
// +spec=`Every Hypothesis inherits its originating Unit deadline; queueing and new executions never renew it`
// +why=`R1 and R2 overlap, so elapsed deadlines preserve unused discovery time without double charging parallel work`
type unitBudget struct {
	ctx                context.Context
	reviewCtx          context.Context
	discoveryCtx       context.Context
	cancel             context.CancelFunc
	cancelReview       context.CancelFunc
	cancelDiscovery    context.CancelFunc
	deadline           time.Time
	reviewEnd          time.Time
	discoveryEnd       time.Time
	discoveryWrapUp    time.Time
	policy             template.ReviewTimeBudget
	mu                 sync.Mutex
	verificationWrapUp time.Time
}

func newUnitBudget(parent context.Context, timeout time.Duration, policy template.ReviewTimeBudget, verification bool) *unitBudget {
	began := time.Now()
	b := &unitBudget{policy: policy.WithDefaults()}
	if timeout > 0 {
		b.ctx, b.cancel = context.WithDeadline(parent, began.Add(timeout))
	} else {
		b.ctx, b.cancel = context.WithCancel(parent)
	}
	b.deadline, _ = b.ctx.Deadline()
	if b.deadline.IsZero() {
		b.reviewCtx, b.cancelReview = context.WithCancel(b.ctx)
		b.discoveryCtx, b.cancelDiscovery = context.WithCancel(b.reviewCtx)
		return b
	}
	total := max(b.deadline.Sub(began), 0)
	// Trial is local and incremental. Reserve a small bounded tail for its
	// final decisions and persistence, rather than a separate model phase.
	reserve := min(total/100, 2*time.Second)
	b.reviewEnd = b.deadline.Add(-reserve)
	duration := b.reviewEnd.Sub(began)
	discovery := duration
	if verification {
		discovery = time.Duration(float64(duration) * b.policy.Review1Fraction)
	}
	b.discoveryEnd = began.Add(discovery)
	b.discoveryWrapUp = began.Add(time.Duration(float64(discovery) * b.policy.ExplorationFraction))
	b.verificationWrapUp = b.discoveryEnd.Add(time.Duration(float64(b.reviewEnd.Sub(b.discoveryEnd)) * b.policy.ExplorationFraction))
	b.reviewCtx, b.cancelReview = context.WithDeadline(b.ctx, b.reviewEnd)
	b.discoveryCtx, b.cancelDiscovery = context.WithDeadline(b.reviewCtx, b.discoveryEnd)
	return b
}

// finishDiscovery redistributes the actual remaining interval across R2's
// exploration and wrap-up. It updates already-running executions as well as
// queued ones; no individual Hypothesis receives a fresh time allowance.
func (b *unitBudget) finishDiscovery() {
	b.cancelDiscovery()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.reviewEnd.IsZero() {
		now := time.Now()
		b.verificationWrapUp = now.Add(time.Duration(float64(max(b.reviewEnd.Sub(now), 0)) * b.policy.ExplorationFraction))
	}
}

func (b *unitBudget) wrapUpDeadline() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.verificationWrapUp
}

func (b *unitBudget) close() {
	b.cancelDiscovery()
	b.cancelReview()
	b.cancel()
}
