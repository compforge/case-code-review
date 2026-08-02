package viewer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReviewStage is the stable viewer vocabulary for the two review loops. Scan
// remains separate because it does not run the diff review's Hypothesis stage.
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
// is keyed by Lane. Calls stays in transcript order so the
// detail page can replay prompt growth across the agent loop.
type ReviewRun struct {
	ID                string   // scope id: unit.ID, run-level phase ID, or scan file path
	Kind              string   // "unit" | "run" | "file"
	Scope             string   // func/file/callchain (units) | filter | scan
	Paths             []string // member file(s)
	FilePath          string   // representative path
	EncodedRepo       string   // encoded repository path used by viewer links
	SessionID         string   // owning session used by viewer links
	Stage             ReviewStage
	Tasks             map[TaskType][]*TaskCard
	Calls             []*TaskCard
	Turns             []*TaskCard
	Conversation      []ConversationNode
	Metrics           ReviewMetrics
	Tools             []ToolUsage
	SourcePreloads    []string
	ContextPaths      map[string][]string
	HasSourcePreloads bool
	HasContextPaths   bool
	FileReads         FileReadMetrics

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

// ConversationNode is one selectable event in the compact agent-loop view.
// Tool calls are children of the assistant turn that requested them; their
// result stays on the same node so the left rail remains easy to scan.
type ConversationNode struct {
	ID               string
	Kind             string
	Label            string
	Preview          string
	Text             string
	Reasoning        string
	Arguments        string
	Result           string
	ToolCallID       string
	Model            string
	StopReason       string
	Error            string
	TurnNo           int
	Depth            int
	DurationMs       int64
	PromptTokens     int
	CompletionTokens int
	OK               bool
	HasResult        bool
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
	r.Conversation = buildConversation(r.Turns)
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

func buildConversation(turns []*TaskCard) []ConversationNode {
	var nodes []ConversationNode
	seenMessages := make(map[string]int)
	nextID := 1
	appendNode := func(node ConversationNode) {
		node.ID = fmt.Sprintf("conversation-%d", nextID)
		nextID++
		nodes = append(nodes, node)
	}

	for _, turn := range turns {
		requestCounts := make(map[string]int)
		for _, message := range turn.Request {
			role := strings.ToLower(message.Role)
			if role != "system" && role != "user" {
				continue
			}
			key := role + "\x00" + message.Text
			requestCounts[key]++
			if requestCounts[key] <= seenMessages[key] {
				continue
			}
			appendNode(ConversationNode{
				Kind:    role,
				Label:   strings.ToUpper(role[:1]) + role[1:],
				Preview: firstLine(message.Text),
				Text:    message.Text,
			})
		}
		for key, count := range requestCounts {
			if count > seenMessages[key] {
				seenMessages[key] = count
			}
		}

		preview := firstLine(turn.ResponseContent)
		if preview == "" {
			preview = firstLine(turn.Reasoning)
		}
		if preview == "" && len(turn.ToolCalls) > 0 {
			preview = fmt.Sprintf("requested %d tool call(s)", len(turn.ToolCalls))
		}
		appendNode(ConversationNode{
			Kind:             "assistant",
			Label:            fmt.Sprintf("Assistant · Turn %d", turn.TurnNo),
			Preview:          preview,
			Text:             turn.ResponseContent,
			Reasoning:        turn.Reasoning,
			Model:            turn.Model,
			StopReason:       turn.StopReason,
			Error:            turn.Error,
			TurnNo:           turn.TurnNo,
			DurationMs:       turn.DurationMs,
			PromptTokens:     turn.PromptTokens,
			CompletionTokens: turn.CompletionTokens,
		})

		for _, call := range turn.ToolCalls {
			appendNode(ConversationNode{
				Kind:       "tool",
				Label:      call.Name,
				Preview:    toolTarget(call.Arguments),
				Arguments:  call.Arguments,
				Result:     call.Result,
				ToolCallID: call.ID,
				TurnNo:     turn.TurnNo,
				Depth:      1,
				DurationMs: call.DurationMs,
				OK:         call.Ok,
				HasResult:  call.HasResult,
			})
		}
	}
	return nodes
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}

func toolTarget(arguments string) string {
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) == nil {
		for _, key := range []string{"file_path", "search_text", "query_name", "path"} {
			if value, ok := args[key].(string); ok && value != "" {
				return value
			}
		}
	}
	return firstLine(arguments)
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
