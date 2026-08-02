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

type ReviewResult struct {
	Assessments      []Assessment
	EvidenceReceipts []EvidenceReceipt
	Execution        harness.ExecutionResult
}

// Review performs the convergent, read-only evidence loop for one Hypothesis.
// It produces an Assessment only; deterministic delivery belongs to trial.
func Review(
	ctx context.Context,
	config Config,
	input ReviewInput,
	continueFrom *harness.ExecutionResult,
) ReviewResult {
	if len(config.Task.Messages) == 0 || input.Hypothesis.ID == "" {
		return ReviewResult{}
	}

	messages := make([]msg.Msg, 0, len(config.Task.Messages))
	for _, message := range config.Task.Messages {
		if continueFrom != nil && message.Role != "user" {
			continue
		}
		full := renderReviewPrompt(message.Content, config, input, false, continueFrom != nil)
		if message.Role != "user" {
			messages = append(messages, msg.Text(message.Role, full))
			continue
		}
		condensed := renderReviewPrompt(message.Content, config, input, true, continueFrom != nil)
		messages = append(messages, newHypothesisMessage(full, condensed))
	}
	messages = append(messages, reviewContextMessages(input)...)

	if config.Task.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(config.Task.Timeout)*time.Second)
		defer cancel()
	}

	collector := NewAssessmentCollector(input.Hypothesis.ID)
	seedReceipts := UnitReceipts(input.Unit)
	seedReceipts = append(seedReceipts, input.PriorEvidence...)
	evidence := &EvidenceLedger{receipts: seedReceipts}
	assessmentHook := &AssessmentHook{
		Collector: collector, Evidence: evidence, LaneID: input.LaneID,
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
			Unit:        &input.Unit,
		},
		Session: config.Session,
		Scope: session.Scope{
			ID: "hypothesis_review:" + input.LaneID, Kind: "lane",
			Type: "hypothesis_review", Paths: input.Paths(),
		},
		TaskType:                session.HypothesisReviewTask,
		Events:                  config.Events,
		MaxTurns:                config.MaxTurns,
		MaxTokens:               config.MaxTokens,
		ContextWindow:           config.MaxTokens,
		FileDedupEnabled:        config.FileDedup,
		FileEvictEnabled:        config.FileEvict,
		WrapUpPrompt:            WrapUpPrompt,
		WrapUpAllowedTools:      []string{SubmitAssessments.Name()},
		CompletionTool:          SubmitAssessments.Name(),
		CompletionPrompt:        "The review is not complete until the current hypothesis has a valid assessment. Submit it with submit_assessments, using insufficient/unknown where evidence is incomplete.",
		CompressionSystemPrompt: config.CompressionSystemPrompt,
		CompressionPrompt:       config.CompressionPrompt,
		CompressionUpdatePrompt: config.CompressionPrompt,
		CompressionPrefixPrompt: config.CompressionPrompt,
		ContinueFrom:            continueFrom,
	})
	if err != nil {
		fmt.Fprintf(console.Out(), "[ccr] Hypothesis review setup failed: %v\n", err)
		warn(config, "hypothesis_review_error", fmt.Sprintf("hypothesis %s in lane %s: %v", input.Hypothesis.ID, input.LaneID, err))
		return ReviewResult{}
	}
	result, err := execution.Run(ctx)
	if config.RecordUsage != nil {
		config.RecordUsage(&result.Usage)
	}
	if err != nil {
		fmt.Fprintf(console.Out(), "[ccr] Hypothesis review failed: %v\n", err)
		warn(config, "hypothesis_review_error", fmt.Sprintf("hypothesis %s in lane %s: %v; accepted assessments were retained", input.Hypothesis.ID, input.LaneID, err))
	}
	if result.State != harness.OutcomeCompleted {
		warn(config, "hypothesis_review_incomplete", fmt.Sprintf("hypothesis %s in lane %s ended incomplete (%s)", input.Hypothesis.ID, input.LaneID, result.Reason))
	}

	assessments := collector.Assessments()
	if len(assessments) == 0 {
		warn(config, "hypothesis_unassessed", fmt.Sprintf("hypothesis %s in lane %s was not assessed and will not pass Trial", input.Hypothesis.ID, input.LaneID))
	}
	return ReviewResult{
		Assessments: assessments, EvidenceReceipts: evidence.Receipts(), Execution: result,
	}
}

func renderReviewPrompt(
	source string,
	config Config,
	input ReviewInput,
	condensed bool,
	retainedLaneContext bool,
) string {
	hypothesisJSON, _ := json.Marshal(input.Hypothesis)
	changesJSON, _ := json.Marshal(reviewChangeSet(input.Unit))
	clues := reviewClues(input.Unit.Clues)
	if condensed {
		for i := range clues {
			clues[i].Text = ""
		}
	}
	cluesJSON, _ := json.Marshal(clues)
	priorContext := "Earlier Lane assessments and evidence remain in the retained conversation above; they are not repeated in this turn."
	if !retainedLaneContext {
		priorJSON, _ := json.Marshal(input.PriorAssessments)
		priorContext = "```json\n" + string(priorJSON) + "\n```"
	}
	evidencePathsJSON, _ := json.Marshal(input.Paths())

	content := source
	content = strings.ReplaceAll(content, "{{change_set}}", string(changesJSON))
	content = strings.ReplaceAll(content, "{{hypothesis}}", string(hypothesisJSON))
	content = strings.ReplaceAll(content, "{{clues}}", string(cluesJSON))
	content = strings.ReplaceAll(content, "{{evidence_paths}}", string(evidencePathsJSON))
	content = strings.ReplaceAll(content, "{{prior_assessments}}", priorContext)
	content = strings.ReplaceAll(content, "{{requirement_background}}", config.Background)
	content = strings.ReplaceAll(content, "{{system_rules}}", reviewRule(input.Hypothesis, config.ResolveRule))
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

func reviewChangeSet(reviewUnit unit.Unit) []reviewChange {
	out := make([]reviewChange, 0, len(reviewUnit.Fragments))
	for _, fragment := range reviewUnit.Fragments {
		status := fragment.Status
		if status == "" {
			status = "changed"
		}
		out = append(out, reviewChange{
			Path: fragment.Path, Status: status,
			Insertions: fragment.Insertions, Deletions: fragment.Deletions,
		})
	}
	return out
}

func reviewRule(hypothesis unitreview.Hypothesis, resolve func(string) string) string {
	if resolve == nil {
		return ""
	}
	rule := resolve(strings.ToLower(hypothesis.Path))
	if rule == "" {
		return ""
	}
	return hypothesis.Path + ":\n" + rule
}
