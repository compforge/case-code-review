package hypothesisreview

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type Config struct {
	Task                    template.LlmConversation
	LLMClient               llm.LLMClient
	Model                   string
	ToolDefs                []llm.ToolDef
	Tools                   *tool.Registry
	Session                 *session.SessionHistory
	Background              string
	MaxTurns                int
	MaxTokens               int
	FileDedup               bool
	FileEvict               bool
	CompressionSystemPrompt string
	CompressionPrompt       string
	ResolveRule             func(path string) string
	RecordUsage             func(*llm.UsageInfo)
	RecordWarning           func(warningType, file, message string)
	OnAssessment            func(AssessmentSubmission)
	Events                  harness.EventSink
}

// Review performs the convergent, read-only evidence loop for one Dossier.
// It produces Assessments only; deterministic delivery belongs to trial.
func Review(ctx context.Context, config Config, dossier Dossier) []Assessment {
	if len(config.Task.Messages) == 0 || len(dossier.Hypotheses) == 0 {
		return nil
	}

	messages := make([]msg.Msg, 0, len(config.Task.Messages))
	for _, message := range config.Task.Messages {
		full := renderDossierPrompt(message.Content, config, dossier, false)
		if message.Role != "user" {
			messages = append(messages, msg.Text(message.Role, full))
			continue
		}
		condensed := renderDossierPrompt(message.Content, config, dossier, true)
		messages = append(messages, newDossierMessage(full, condensed))
	}

	if config.Task.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(config.Task.Timeout)*time.Second)
		defer cancel()
	}

	expected := make([]string, 0, len(dossier.Hypotheses))
	for _, hypothesis := range dossier.Hypotheses {
		expected = append(expected, hypothesis.ID)
	}
	collector := NewAssessmentCollector(expected...)
	evidence := &EvidenceLedger{}
	assessmentHook := &AssessmentHook{
		Collector: collector, Evidence: evidence, DossierID: dossier.ID,
		OnAccepted: config.OnAssessment,
	}
	execution, err := harness.NewExecution(harness.ExecutionSpec{
		LLMClient: config.LLMClient,
		Model:     config.Model,
		Messages:  messages,
		ToolDefs:  ToolDefs(config.ToolDefs),
		Tools:     config.Tools,
		ToolHandler: &ReviewHandler{
			Assessments: assessmentHook,
			Evidence:    evidence,
			Tools:       config.Tools,
		},
		Session: config.Session,
		Scope: session.Scope{
			ID: "hypothesis_review:" + dossier.ID, Kind: "run",
			Type: "hypothesis_review", Paths: dossier.Paths(),
		},
		TaskType:           session.HypothesisReviewTask,
		Events:             config.Events,
		MaxTurns:           config.MaxTurns,
		MaxTokens:          config.MaxTokens,
		ContextWindow:      config.MaxTokens,
		FileDedupEnabled:   config.FileDedup,
		FileEvictEnabled:   config.FileEvict,
		WrapUpPrompt:       WrapUpPrompt,
		WrapUpAllowedTools: []string{SubmitAssessments.Name(), tool.TaskDone.Name()},
		CompletionPrompt:   "The review is not complete until every supplied hypothesis has an assessment. Submit the remaining assessments, then call task_done.",
		CompletionCheck: func(context.Context) (bool, string) {
			missing := collector.Missing()
			if len(missing) == 0 {
				return true, ""
			}
			return false, fmt.Sprintf("Cannot finish: assessments are still missing for %s. Submit them with submit_assessments, using insufficient/unknown where evidence is incomplete.", strings.Join(missing, ", "))
		},
		CompressionSystemPrompt: config.CompressionSystemPrompt,
		CompressionPrompt:       config.CompressionPrompt,
		CompressionUpdatePrompt: config.CompressionPrompt,
		CompressionPrefixPrompt: config.CompressionPrompt,
	})
	if err != nil {
		fmt.Fprintf(console.Out(), "[ccr] Hypothesis review setup failed: %v\n", err)
		warn(config, "hypothesis_review_error", fmt.Sprintf("dossier %s: %v", dossier.ID, err))
		return nil
	}
	result, err := execution.Run(ctx)
	if config.RecordUsage != nil {
		config.RecordUsage(&result.Usage)
	}
	if err != nil {
		fmt.Fprintf(console.Out(), "[ccr] Hypothesis review failed: %v\n", err)
		warn(config, "hypothesis_review_error", fmt.Sprintf("dossier %s: %v; accepted assessments were retained", dossier.ID, err))
	}
	if result.State != harness.OutcomeCompleted {
		warn(config, "hypothesis_review_incomplete", fmt.Sprintf("dossier %s ended incomplete (%s)", dossier.ID, result.Reason))
	}

	assessments := collector.Assessments()
	total := uniqueHypothesisCount(dossier.Hypotheses)
	if missing := total - assessedHypothesisCount(dossier.Hypotheses, assessments); missing > 0 {
		warn(config, "hypothesis_unassessed", fmt.Sprintf("dossier %s: %d of %d hypotheses were not assessed and will not pass Trial", dossier.ID, missing, total))
	}
	return assessments
}

func renderDossierPrompt(source string, config Config, dossier Dossier, condensed bool) string {
	hypothesesJSON, _ := json.Marshal(dossier.Hypotheses)
	changesJSON, _ := json.Marshal(reviewChangeSet(dossier.Changes))
	clues := reviewClues(dossier.Clues)
	if condensed {
		for i := range clues {
			clues[i].Text = ""
		}
	}
	cluesJSON, _ := json.Marshal(clues)
	priorJSON, _ := json.Marshal(map[string]any{
		"dossier_ids": dossier.PriorDossierIDs,
		"assessments": dossier.PriorAssessments,
	})
	evidencePathsJSON, _ := json.Marshal(dossier.EvidencePaths)

	content := source
	content = strings.ReplaceAll(content, "{{dossier}}", string(changesJSON))
	content = strings.ReplaceAll(content, "{{change_set}}", string(changesJSON))
	content = strings.ReplaceAll(content, "{{hypotheses}}", string(hypothesesJSON))
	content = strings.ReplaceAll(content, "{{clues}}", string(cluesJSON))
	content = strings.ReplaceAll(content, "{{evidence_paths}}", string(evidencePathsJSON))
	content = strings.ReplaceAll(content, "{{prior_assessments}}", string(priorJSON))
	content = strings.ReplaceAll(content, "{{requirement_background}}", config.Background)
	content = strings.ReplaceAll(content, "{{system_rules}}", reviewRules(dossier.Hypotheses, config.ResolveRule))
	return content
}

func warn(config Config, warningType, message string) {
	if config.RecordWarning != nil {
		config.RecordWarning(warningType, "", message)
	}
}

type reviewClue struct {
	Kind     string `json:"kind"`
	Relation string `json:"relation"`
	Ref      string `json:"ref,omitempty"`
	Text     string `json:"text,omitempty"`
}

func reviewClues(clues []unit.Clue) []reviewClue {
	out := make([]reviewClue, 0, len(clues))
	for _, clue := range clues {
		out = append(out, reviewClue{
			Kind: string(clue.Kind), Relation: string(clue.Relation), Ref: clue.Ref, Text: clue.Text,
		})
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

func reviewRules(hypotheses []unitreview.Hypothesis, resolve func(string) string) string {
	if resolve == nil {
		return ""
	}
	var blocks []string
	seen := make(map[string]bool)
	for _, hypothesis := range hypotheses {
		rule := resolve(strings.ToLower(hypothesis.Path))
		if rule == "" || seen[rule] {
			continue
		}
		seen[rule] = true
		blocks = append(blocks, hypothesis.Path+":\n"+rule)
	}
	return strings.Join(blocks, "\n\n")
}

func assessedHypothesisCount(hypotheses []unitreview.Hypothesis, assessments []Assessment) int {
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

func uniqueHypothesisCount(hypotheses []unitreview.Hypothesis) int {
	seen := make(map[string]bool, len(hypotheses))
	for _, hypothesis := range hypotheses {
		seen[hypothesis.ID] = true
	}
	return len(seen)
}
