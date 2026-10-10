package runner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/trial"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// unitRun tracks work accepted before R1 seals its output. The map is formed
// before dispatch; each entry starts its clock only after acquiring an R1 slot.
type unitRun struct {
	budget       *unitBudget
	pending      sync.WaitGroup
	resolving    sync.WaitGroup
	mu           sync.Mutex
	review2State string
}

func (a *Runner) finishHypothesis(input hypothesisreview.ReviewInput, result hypothesisreview.ReviewResult) {
	run := a.unitRuns[input.Unit.ID]
	defer run.pending.Done()
	assessed := false
	for _, assessment := range result.Assessments {
		if assessment.HypothesisID == input.Hypothesis.ID {
			assessed = true
			break
		}
	}
	if assessed {
		return
	}
	state := result.Execution.State
	if state == "" || state == harness.OutcomeCompleted {
		state = harness.OutcomeTruncated
	}
	run.mu.Lock()
	if run.review2State == "" || state == harness.OutcomeTimeout {
		run.review2State = state
	}
	run.mu.Unlock()
	a.recordWarning("hypothesis_unassessed", input.Hypothesis.Path,
		fmt.Sprintf("unit %s hypothesis %s ended %s before assessment: %s", input.Unit.ID, input.Hypothesis.ID, state, result.Execution.Reason))
	a.session.WriteArtifactContext(run.budget.ctx, "hypothesis_review_incomplete", map[string]any{
		"unit_id": input.Unit.ID, "hypothesis_id": input.Hypothesis.ID, "lane_id": input.LaneID,
		"outcome": state, "reason": result.Execution.Reason,
	})
}

func (a *Runner) runUnitPipeline(parent context.Context, u unit.Unit, run *unitRun, gate *trial.Gate, releaseDiscovery func()) {
	ctx, finish := session.Begin(parent, "review.pipeline", timeline.Attribute{Key: "unit_id", Value: u.ID})
	run.budget = newUnitBudget(ctx, time.Duration(a.args.ConcurrentTaskTimeout)*time.Minute, a.args.Template.ReviewTimeBudget, a.features.Enabled(feature.HypothesisReview))
	defer run.budget.close()
	a.session.WriteArtifactContext(ctx, "unit_budget", map[string]any{
		"unit_id": u.ID, "deadline": run.budget.deadline,
		"review1_deadline": run.budget.discoveryEnd, "review1_wrap_up": run.budget.discoveryWrapUp,
		"review2_deadline": run.budget.reviewEnd, "review2_wrap_up": run.budget.wrapUpDeadline(),
	})
	deb := session.Debrief{Formed: string(u.Formed)}
	var reviewErr error
	defer func() {
		if p := recover(); p != nil {
			reviewErr = fmt.Errorf("unit %s panic: %v", u.ID, p)
			deb.Outcome, deb.Reason = "panic", reviewErr.Error()
		}
		run.resolving.Wait()
		if err := run.budget.discoveryCtx.Err(); err != nil {
			reviewErr = errors.Join(reviewErr, err)
			deb.Outcome, deb.Reason = contextOutcome(err), "Review 1: "+err.Error()
		} else if deb.Outcome == "" {
			deb.Outcome = harness.OutcomeLLMError
			if reviewErr != nil {
				deb.Reason = reviewErr.Error()
			}
		}
		run.budget.finishDiscovery()
		// R2 may still be active. Release the producer slot before waiting,
		// otherwise --concurrency would accidentally serialize the pipeline.
		releaseDiscovery()
		a.session.WriteArtifactContext(ctx, "unit_review1_completed", map[string]any{
			"unit_id": u.ID, "outcome": deb.Outcome, "reason": deb.Reason,
			"review2_wrap_up": run.budget.wrapUpDeadline(),
		})
		run.pending.Wait()
		if gate != nil {
			snapshot := u.Review()
			if a.features.Enabled(feature.HypothesisReview) && len(snapshot.Assessments) < len(snapshot.Hypotheses) && deb.Outcome == harness.OutcomeCompleted {
				deb.Outcome, deb.Reason = harness.OutcomeTruncated, "Review 2 has unassessed hypotheses"
			}
			trialCtx, finishTrial := session.Begin(run.budget.ctx, "trial.finalize", timeline.Attribute{Key: "unit_id", Value: u.ID})
			var findings []finding.Finding
			var decisions []unit.TrialDecision
			if a.features.Enabled(feature.HypothesisReview) {
				findings, decisions = gate.Finalize([]unit.Unit{u})
			} else {
				findings, decisions = gate.Bypass([]unit.Unit{u})
			}
			a.persistTrialDecisions(trialCtx, []unit.Unit{u}, decisions)
			for _, finding := range findings {
				a.deliverFinding(trialCtx, finding, []unit.Unit{u})
			}
			finishTrial(run.budget.ctx.Err())
		}
		run.mu.Lock()
		review2State := run.review2State
		run.mu.Unlock()
		if (deb.Outcome == harness.OutcomeCompleted || deb.Outcome == harness.OutcomeTruncated) && review2State != "" {
			deb.Outcome, deb.Reason = review2State, "Review 2 has unassessed hypotheses"
		}
		a.session.Flush()
		if err := run.budget.ctx.Err(); err != nil {
			deb.Outcome, deb.Reason = contextOutcome(err), "Unit pipeline: "+err.Error()
		}
		if deb.Outcome != harness.OutcomeCompleted {
			atomic.AddInt64(&a.unitFailed, 1)
			a.recordWarning("unit_incomplete", u.Path(), fmt.Sprintf("unit %s: %s (%s)", u.ID, deb.Outcome, deb.Reason))
			reviewErr = errors.Join(reviewErr, fmt.Errorf("%s: %s", deb.Outcome, deb.Reason))
		}
		a.session.CloseScope(session.Scope{ID: u.ID, Kind: "unit", Type: string(u.Scope), Paths: u.Paths()}, deb)
		a.session.Flush()
		finish(reviewErr)
	}()
	deb, reviewErr = a.reviewUnit(run.budget.discoveryCtx, u, run.budget.discoveryWrapUp)
}

func contextOutcome(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return harness.OutcomeTimeout
	}
	return harness.OutcomeAborted
}
