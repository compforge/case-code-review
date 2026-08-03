package runner

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"

	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// reviewHypothesis delegates one claim to the convergent Review 2 loop. Lane
// assignment and scheduling stay in Runner; the review package owns only
// evidence gathering and Assessment production.
func (a *Runner) reviewHypothesis(
	ctx context.Context,
	input hypothesisreview.ReviewInput,
	continueFrom *harness.ExecutionResult,
) (result hypothesisreview.ReviewResult) {
	task := a.args.Template.HypothesisReviewTask
	if task == nil || len(task.Messages) == 0 || input.Hypothesis.ID == "" {
		return hypothesisreview.ReviewResult{}
	}
	a.persistHypothesisReviewStart(input)
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(console.Out(), "[ccr] Hypothesis review panic for %s in %s: %v\n%s\n", input.Hypothesis.ID, input.LaneID, recovered, debug.Stack())
			telemetry.ErrorEvent(ctx, "hypothesis_review.panic", fmt.Errorf("panic: %v", recovered))
			a.recordWarning("hypothesis_review_error", "", fmt.Sprintf("hypothesis %s in lane %s panic: %v", input.Hypothesis.ID, input.LaneID, recovered))
			result = hypothesisreview.ReviewResult{}
		}
	}()
	result = hypothesisreview.Review(ctx, hypothesisreview.Config{
		Task:                    *task,
		LLMClient:               a.args.LLMClient,
		Model:                   a.args.Model,
		ToolDefs:                a.args.MainToolDefs,
		Tools:                   a.args.Tools,
		Session:                 a.session,
		Background:              a.args.Background,
		MaxTurns:                a.args.Template.MaxToolRequestTimes,
		MaxTokens:               a.args.Template.MaxTokens,
		FileDedup:               a.features.Enabled(feature.FileDedup),
		FileEvict:               a.features.Enabled(feature.FileEvict),
		CompressionSystemPrompt: a.executor.CompressionSystemPrompt(),
		CompressionPrompt:       a.executor.CompressionPrompt(),
		ResolveRule:             a.resolveSystemRule,
		RecordUsage:             a.executor.RecordUsage,
		RecordWarning:           a.recordWarning,
		OnAssessment: func(submission hypothesisreview.AssessmentSubmission) {
			a.persistAssessmentSubmission(input, submission)
		},
		Events: a.hypothesisReviewEvents(ctx),
	}, input, continueFrom)
	a.persistHypothesisReviewExecution(input, result.Execution)
	return result
}

func (a *Runner) persistHypothesisReviewStart(input hypothesisreview.ReviewInput) {
	a.session.WriteArtifact("hypothesis_review_start", map[string]any{
		"hypothesis_id": input.Hypothesis.ID,
		"origin_unit":   input.Hypothesis.OriginUnit,
		"lane_id":       input.LaneID,
	})
}

func collectReviewClues(units []unit.Unit) []unit.Clue {
	seen := make(map[string]bool)
	var out []unit.Clue
	for _, reviewUnit := range units {
		for _, clue := range reviewUnit.Clues {
			key := strings.Join([]string{
				string(clue.Kind), string(clue.Relation), clue.Ref, clue.Text,
			}, "\x00")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, clue)
		}
	}
	return out
}

func (a *Runner) persistLaneAssignment(input hypothesisreview.ReviewInput, reason string) {
	a.session.WriteArtifact("review_lane_assignment", map[string]any{
		"lane_id": input.LaneID, "hypothesis_id": input.Hypothesis.ID,
		"origin_unit": input.Hypothesis.OriginUnit,
		"paths":       input.Paths(), "assigned_by": reason,
	})
}

func (a *Runner) persistAssessmentSubmission(
	input hypothesisreview.ReviewInput,
	submission hypothesisreview.AssessmentSubmission,
) {
	assessment := submission.Assessment
	a.session.WriteArtifact("review_assessment", map[string]any{
		"lane_id":          assessment.LaneID,
		"origin_unit":      input.Hypothesis.OriginUnit,
		"submission_index": assessment.SubmissionIndex,
		"replaced":         submission.Replaced,
		"hypothesis_id":    assessment.HypothesisID,
		"support":          assessment.Support, "attribution": assessment.Attribution,
		"value": assessment.Value, "novelty": assessment.Novelty,
		"reason": assessment.Reason, "evidence": assessment.Evidence,
		"evidence_receipts": assessment.EvidenceReceipts,
		"reviewer_alias":    assessment.ReviewerAlias,
	})
}

// persistHypothesisReviewExecution bridges the domain claim to the generic
// Harness Execution. Execution records stay domain-free; this artifact lets
// observers join Lane queueing, Assessment production, and model-loop cost.
func (a *Runner) persistHypothesisReviewExecution(
	input hypothesisreview.ReviewInput,
	execution harness.ExecutionResult,
) {
	if execution.ID == "" {
		return
	}
	a.session.WriteArtifact("hypothesis_review_execution", map[string]any{
		"execution_id":  execution.ID,
		"hypothesis_id": input.Hypothesis.ID,
		"origin_unit":   input.Hypothesis.OriginUnit,
		"lane_id":       input.LaneID,
		"outcome":       execution.State,
		"reason":        execution.Reason,
		"duration_ms":   execution.Duration.Milliseconds(),
	})
}

func (a *Runner) hypothesisReviewEvents(ctx context.Context) harness.EventSink {
	return harness.EventSinkFunc(func(event harness.ExecutionEvent) {
		switch event.Type {
		case harness.EventModelResponse:
			a.executor.RecordModel(event.Alias)
			model := event.Model
			if model == "" {
				model = a.args.Model
			}
			var tokens int64
			if event.Usage != nil {
				tokens = event.Usage.TotalTokens
			}
			telemetry.RecordLLMRequest(ctx, model, event.Duration, tokens, "ok")
		case harness.EventToolStart:
			if event.Tool != tool.TaskDone.Name() {
				a.executor.RecordToolCall(event.Tool)
			}
		}
	})
}
