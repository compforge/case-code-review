package viewer

import (
	"sort"
	"time"
)

// ReviewStage is the stable viewer vocabulary for the two review loops. Scan
// remains separate because it does not run the diff review's CaseFile stage.
type ReviewStage string

const (
	Review1Stage ReviewStage = "review1"
	Review2Stage ReviewStage = "review2"
	ScanStage    ReviewStage = "scan"
	OtherStage   ReviewStage = "other"
)

func (s ReviewStage) Label() string {
	switch s {
	case Review1Stage:
		return "Review 1"
	case Review2Stage:
		return "Review 2"
	case ScanStage:
		return "Scan"
	default:
		return "Other"
	}
}

// ReviewRun aggregates one review scope. Review 1 is keyed by Unit; Review 2
// is keyed by CaseFile/run scope. Calls stays in transcript order so the
// detail page can replay prompt growth across the agent loop.
type ReviewRun struct {
	ID              string   // scope id: unit.ID, run-level phase ID, or scan file path
	Kind            string   // "unit" | "run" | "file"
	Scope           string   // func/file/callchain (units) | filter | scan
	Paths           []string // member file(s)
	FilePath        string   // representative path
	EncodedRepo     string   // encoded repository path used by viewer links
	SessionID       string   // owning session used by viewer links
	Stage           ReviewStage
	Tasks           map[TaskType][]*TaskCard
	Calls           []*TaskCard
	Turns           []*TaskCard
	Metrics         ReviewMetrics
	Tools           []ToolUsage
	Materials       []string
	ContextPaths    map[string][]string
	HasMaterials    bool
	HasContextPaths bool
	FileReads       FileReadMetrics

	startedAt  time.Time
	finishedAt time.Time
}

// ReviewMetrics is the per-Review counterpart of TokenUsageSummary. Elapsed
// is wall time from the first to last scoped event; LLM duration is the sum of
// model-call durations and can exceed elapsed when work overlaps.
type ReviewMetrics struct {
	ElapsedSec       float64
	LLMDurationMs    int64
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
	LLMCalls         int
	TurnCount        int
	ToolCalls        int
	ToolFailures     int
	MaxPromptTokens  int
}

// ToolUsage aggregates calls to one tool at either review or session level.
type ToolUsage struct {
	Name       string
	Calls      int
	Failures   int
	DurationMs int64
}

func (r *ReviewRun) observeTimestamp(raw any) {
	s, _ := raw.(string)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return
	}
	if r.startedAt.IsZero() || t.Before(r.startedAt) {
		r.startedAt = t
	}
	if r.finishedAt.IsZero() || t.After(r.finishedAt) {
		r.finishedAt = t
	}
}

func finalizeReview(r *ReviewRun) {
	r.Stage = classifyReview(r)
	toolIdx := map[string]*ToolUsage{}
	previousPrompt := 0
	previousMessages := 0

	for _, card := range r.Calls {
		r.Metrics.LLMCalls++
		r.Metrics.LLMDurationMs += card.DurationMs
		r.Metrics.PromptTokens += card.PromptTokens
		r.Metrics.CompletionTokens += card.CompletionTokens
		r.Metrics.CacheReadTokens += card.CacheReadTokens
		r.Metrics.CacheWriteTokens += card.CacheWriteTokens
		if isAgentTurn(r.Stage, card.TaskType) {
			card.TurnNo = len(r.Turns) + 1
			card.PromptDelta = card.PromptTokens - previousPrompt
			card.MessageDelta = len(card.Request) - previousMessages
			if card.PromptTokens > r.Metrics.MaxPromptTokens {
				r.Metrics.MaxPromptTokens = card.PromptTokens
			}
			previousPrompt = card.PromptTokens
			previousMessages = len(card.Request)
			r.Turns = append(r.Turns, card)
		}

		for _, call := range card.ToolCalls {
			tool := toolIdx[call.Name]
			if tool == nil {
				tool = &ToolUsage{Name: call.Name}
				toolIdx[call.Name] = tool
			}
			tool.Calls++
			tool.DurationMs += call.DurationMs
			if !call.Ok {
				tool.Failures++
			}
		}
	}

	r.Metrics.TurnCount = len(r.Turns)
	r.Tools = sortedTools(toolIdx)
	for _, tool := range r.Tools {
		r.Metrics.ToolCalls += tool.Calls
		r.Metrics.ToolFailures += tool.Failures
	}
	if !r.startedAt.IsZero() && !r.finishedAt.IsZero() {
		r.Metrics.ElapsedSec = r.finishedAt.Sub(r.startedAt).Seconds()
	}
	if r.Metrics.ElapsedSec == 0 && r.Metrics.LLMDurationMs > 0 {
		r.Metrics.ElapsedSec = float64(r.Metrics.LLMDurationMs) / 1000
	}
	if r.Stage == Review1Stage {
		r.FileReads = analyzeFileReads(r)
	}
}

func classifyReview(r *ReviewRun) ReviewStage {
	if r.Kind == "unit" {
		return Review1Stage
	}
	if r.Scope == "hypothesis_review" || len(r.Tasks[HypothesisReviewTask]) > 0 {
		return Review2Stage
	}
	if r.Kind == "file" || r.Scope == "scan" {
		return ScanStage
	}
	return OtherStage
}

func isAgentTurn(stage ReviewStage, taskType TaskType) bool {
	switch stage {
	case Review2Stage:
		return taskType == HypothesisReviewTask
	case Review1Stage, ScanStage:
		return taskType == MainTask
	default:
		return taskType == MainTask || taskType == HypothesisReviewTask
	}
}

func sortedTools(index map[string]*ToolUsage) []ToolUsage {
	tools := make([]ToolUsage, 0, len(index))
	for _, tool := range index {
		tools = append(tools, *tool)
	}
	sort.Slice(tools, func(i, j int) bool {
		if tools[i].Calls != tools[j].Calls {
			return tools[i].Calls > tools[j].Calls
		}
		return tools[i].Name < tools[j].Name
	})
	return tools
}

func reviewStageRank(stage ReviewStage) int {
	switch stage {
	case Review1Stage:
		return 0
	case Review2Stage:
		return 1
	case ScanStage:
		return 2
	default:
		return 3
	}
}

// Review returns one review scope by its stable session scope id.
func (vs *ViewSession) Review(id string) *ReviewRun {
	for _, review := range vs.Reviews {
		if review.ID == id {
			return review
		}
	}
	return nil
}
