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
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(console.Out(), "[ccr] Hypothesis review panic for %s in %s: %v\n%s\n", input.Hypothesis.ID, input.LaneID, recovered, debug.Stack())
			telemetry.ErrorEvent(ctx, "hypothesis_review.panic", fmt.Errorf("panic: %v", recovered))
			a.recordWarning("hypothesis_review_error", "", fmt.Sprintf("hypothesis %s in lane %s panic: %v", input.Hypothesis.ID, input.LaneID, recovered))
			result = hypothesisreview.ReviewResult{}
		}
	}()
	return hypothesisreview.Review(ctx, hypothesisreview.Config{
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
		OnAssessment:            a.persistAssessmentSubmission,
		Events:                  a.hypothesisReviewEvents(ctx),
	}, input, continueFrom)
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
		"paths": input.Paths(), "assigned_by": reason,
	})
}

func (a *Runner) persistAssessmentSubmission(submission hypothesisreview.AssessmentSubmission) {
	assessment := submission.Assessment
	a.session.WriteArtifact("review_assessment", map[string]any{
		"lane_id":          assessment.LaneID,
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
