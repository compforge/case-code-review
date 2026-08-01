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

// runHypothesisReviews transfers the run's complete investigative output into
// one CaseFile, then delegates the convergent loop to Hypothesis Review.
func (a *Runner) runHypothesisReviews(
	ctx context.Context,
	units []unit.Unit,
) (assessments []hypothesisreview.Assessment) {
	task := a.args.Template.HypothesisReviewTask
	if task == nil || len(task.Messages) == 0 {
		if len(a.hypotheses.Hypotheses()) > 0 {
			a.recordWarning(
				"hypothesis_review_unavailable", "",
				"hypotheses cannot pass Trial because HYPOTHESIS_REVIEW_TASK is not configured",
			)
		}
		return nil
	}
	hypotheses := a.hypotheses.Hypotheses()
	if len(hypotheses) == 0 {
		return nil
	}
	caseFile := hypothesisreview.CaseFile{
		ID:         "change_set",
		Changes:    a.changes,
		Hypotheses: hypotheses,
		Clues:      collectCaseFileClues(units),
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			fmt.Fprintf(console.Out(), "[ccr] Hypothesis review panic: %v\n%s\n", recovered, debug.Stack())
			telemetry.ErrorEvent(ctx, "hypothesis_review.panic", fmt.Errorf("panic: %v", recovered))
			a.recordWarning("hypothesis_review_error", "", fmt.Sprintf("panic: %v", recovered))
			assessments = nil
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
		Events:                  a.hypothesisReviewEvents(ctx),
	}, caseFile)
}

func collectCaseFileClues(units []unit.Unit) []unit.Clue {
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
