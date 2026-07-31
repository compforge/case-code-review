package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/review"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// runHypothesisReviews independently assesses the run's complete investigative
// output as one CaseFile. A comment anchor is not a review boundary: hypotheses
// landing in different files may describe the same cross-file defect.
func (a *Runner) runHypothesisReviews(ctx context.Context, units []unit.Unit) []review.Assessment {
	if ft := a.args.Template.HypothesisReviewTask; ft == nil || len(ft.Messages) == 0 {
		if len(a.hypotheses.Hypotheses()) > 0 {
			a.recordWarning(
				"hypothesis_review_unavailable",
				"",
				"hypotheses cannot pass Trial because HYPOTHESIS_REVIEW_TASK is not configured",
			)
		}
		return nil
	}
	hypotheses := a.hypotheses.Hypotheses()
	if len(hypotheses) == 0 {
		return nil
	}
	caseFile := review.CaseFile{
		ID:         "change_set",
		Changes:    a.changes,
		Hypotheses: hypotheses,
		Clues:      collectCaseFileClues(units),
	}
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(console.Out(), "[ccr] Hypothesis review panic: %v\n%s\n", r, debug.Stack())
			telemetry.ErrorEvent(ctx, "hypothesis_review.panic", fmt.Errorf("panic: %v", r))
			a.recordWarning("hypothesis_review_error", "", fmt.Sprintf("panic: %v", r))
		}
	}()
	return a.executeHypothesisReview(ctx, caseFile)
}

// executeHypothesisReview runs a convergent Harness Execution. Its tool
// definitions are structurally limited to read-only evidence gathering plus
// submit_assessments/task_done; it cannot create a new Hypothesis or Finding.
func (a *Runner) executeHypothesisReview(
	ctx context.Context,
	caseFile review.CaseFile,
) []review.Assessment {
	ft := a.args.Template.HypothesisReviewTask
	if ft == nil || len(ft.Messages) == 0 || len(caseFile.Hypotheses) == 0 {
		return nil
	}

	hypothesesJSON, _ := json.Marshal(caseFile.Hypotheses)
	cluesJSON, _ := json.Marshal(reviewClues(caseFile.Clues))
	changeSetJSON, _ := json.Marshal(reviewChangeSet(caseFile.Changes))

	messages := make([]llm.Message, 0, len(ft.Messages))
	for _, m := range ft.Messages {
		content := m.Content
		content = strings.ReplaceAll(content, "{{change_set}}", string(changeSetJSON))
		content = strings.ReplaceAll(content, "{{hypotheses}}", string(hypothesesJSON))
		content = strings.ReplaceAll(content, "{{clues}}", string(cluesJSON))
		content = strings.ReplaceAll(content, "{{requirement_background}}", a.args.Background)
		content = strings.ReplaceAll(content, "{{system_rules}}", a.reviewRules(caseFile.Hypotheses))
		messages = append(messages, llm.NewTextMessage(m.Role, content))
	}

	if ft.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ft.Timeout)*time.Second)
		defer cancel()
	}

	scope := session.Scope{
		ID:   "hypothesis_review:" + caseFile.ID,
		Kind: "run", Type: "hypothesis_review", Paths: caseFile.Paths(),
	}
	collector := review.NewAssessmentCollector()
	result, err := harness.Execute(ctx, harness.ExecutionSpec{
		LLMClient:               a.args.LLMClient,
		Model:                   a.args.Model,
		Messages:                msg.Wrap(messages),
		ToolDefs:                review.HypothesisReviewToolDefs(a.args.MainToolDefs),
		Tools:                   a.args.Tools,
		ToolHandler:             &review.AssessmentHook{Collector: collector},
		Session:                 a.session,
		Scope:                   scope,
		TaskType:                session.HypothesisReviewTask,
		Events:                  a.hypothesisReviewEvents(ctx),
		MaxTurns:                a.args.Template.MaxToolRequestTimes,
		MaxTokens:               a.args.Template.MaxTokens,
		ContextWindow:           a.args.Template.MaxTokens,
		FileDedupEnabled:        a.features.Enabled(feature.FileDedup),
		FileEvictEnabled:        a.features.Enabled(feature.FileEvict),
		WrapUpPrompt:            review.HypothesisReviewWrapUpPrompt,
		CompletionPrompt:        "The review is not complete until every supplied hypothesis has an assessment. Call submit_assessments, then task_done.",
		CompressionSystemPrompt: a.executor.compressionSystemPrompt,
		CompressionPrompt:       a.executor.compressionPrompt,
		CompressionUpdatePrompt: a.executor.compressionPrompt,
		CompressionPrefixPrompt: a.executor.compressionPrompt,
	})
	a.executor.RecordUsage(&result.Usage)
	if err != nil {
		fmt.Fprintf(console.Out(), "[ccr] Hypothesis review failed: %v\n", err)
		a.recordWarning("hypothesis_review_error", "", err.Error())
		return nil
	}
	if result.State != harness.OutcomeCompleted {
		a.recordWarning(
			"hypothesis_review_incomplete",
			"",
			fmt.Sprintf("hypothesis review ended without task_done (%s)", result.Reason),
		)
	}

	assessments := collector.Assessments()
	total := uniqueHypothesisCount(caseFile.Hypotheses)
	if missing := total - assessedHypothesisCount(caseFile.Hypotheses, assessments); missing > 0 {
		a.recordWarning(
			"hypothesis_unassessed",
			"",
			fmt.Sprintf("%d of %d hypotheses were not assessed and will not pass Trial", missing, total),
		)
	}
	return assessments
}

type reviewClue struct {
	Kind     string `json:"kind"`
	Relation string `json:"relation"`
	Ref      string `json:"ref,omitempty"`
	Text     string `json:"text"`
}

func reviewClues(clues unit.Dossier) []reviewClue {
	out := make([]reviewClue, 0, len(clues))
	for _, clue := range clues {
		out = append(out, reviewClue{
			Kind: string(clue.Kind), Relation: string(clue.Relation), Ref: clue.Ref, Text: clue.Text,
		})
	}
	return out
}

func collectCaseFileClues(units []unit.Unit) unit.Dossier {
	seen := make(map[string]bool)
	var out unit.Dossier
	for _, reviewUnit := range units {
		for _, clue := range reviewUnit.Dossier {
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

type reviewChange struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Insertions int64  `json:"insertions"`
	Deletions  int64  `json:"deletions"`
}

func reviewChangeSet(changes []change.Change) []reviewChange {
	out := make([]reviewChange, 0, len(changes))
	for _, changed := range changes {
		status := "modified"
		switch {
		case changed.IsNew:
			status = "added"
		case changed.IsDeleted:
			status = "deleted"
		case changed.IsRenamed:
			status = "renamed"
		}
		out = append(out, reviewChange{
			Path: changed.NewPath, Status: status,
			Insertions: changed.Insertions, Deletions: changed.Deletions,
		})
	}
	return out
}

func (a *Runner) reviewRules(hypotheses []review.Hypothesis) string {
	var blocks []string
	seen := make(map[string]bool)
	for _, hypothesis := range hypotheses {
		rule := a.resolveSystemRule(strings.ToLower(hypothesis.Path))
		if rule == "" || seen[rule] {
			continue
		}
		seen[rule] = true
		blocks = append(blocks, hypothesis.Path+":\n"+rule)
	}
	return strings.Join(blocks, "\n\n")
}

func assessedHypothesisCount(
	hypotheses []review.Hypothesis,
	assessments []review.Assessment,
) int {
	expected := make(map[string]bool, len(hypotheses))
	for _, hypothesis := range hypotheses {
		expected[hypothesis.ID] = true
	}
	seen := make(map[string]bool, len(assessments))
	for _, assessment := range assessments {
		if expected[assessment.HypothesisID] {
			seen[assessment.HypothesisID] = true
		}
	}
	return len(seen)
}

func uniqueHypothesisCount(hypotheses []review.Hypothesis) int {
	seen := make(map[string]bool, len(hypotheses))
	for _, hypothesis := range hypotheses {
		seen[hypothesis.ID] = true
	}
	return len(seen)
}

func (a *Runner) hypothesisReviewEvents(ctx context.Context) harness.EventSink {
	return harness.EventSinkFunc(func(event harness.ExecutionEvent) {
		switch event.Type {
		case harness.EventModelResponse:
			a.executor.recordModel(event.Alias)
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
				a.executor.recordToolCall(event.Tool)
			}
		}
	})
}
