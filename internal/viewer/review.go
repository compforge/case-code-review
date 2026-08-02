package viewer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

// ReviewScope is the Viewer projection of one persisted Scope. A Review 1
// Unit normally owns one Execution; a Review 2 Lane may own many sequential
// Executions that retain context between hypotheses.
type ReviewScope struct {
	ID          string
	Kind        string
	Scope       string
	Paths       []string
	FilePath    string
	EncodedRepo string
	SessionID   string
	Stage       ReviewStage

	Tasks           map[TaskType][]*TaskCard
	Calls           []*TaskCard
	Executions      []*ReviewExecution
	Artifacts       []ReviewArtifact
	Metrics         ReviewMetrics
	Tools           []ToolUsage
	ExecutionDone   int
	ExecutionMissed int
	Status          string // completed | incomplete; aggregate projection only

	SourcePreloads    []string
	ContextPaths      map[string][]string
	HasSourcePreloads bool
	HasContextPaths   bool
	FileReads         FileReadMetrics
}

// ReviewExecution is one actual Harness/AgentGo run. Its terminal state comes
// only from execution_end; Scope and tool names never imply completion.
type ReviewExecution struct {
	ID           string
	TaskType     TaskType
	Tasks        map[TaskType][]*TaskCard
	Calls        []*TaskCard
	Turns        []*TaskCard
	Conversation []ConversationNode
	Metrics      ReviewMetrics
	Tools        []ToolUsage
	Status       string // completed | incomplete
	Outcome      string
	Reason       string
	DurationMs   int64
	ToolCalls    int
	ToolErrors   int
}

// ReviewMetrics is the per-Scope/Execution counterpart of TokenUsageSummary.
// LLM duration is the sum of model-call durations and can exceed wall time
// when calls overlap.
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

// ToolUsage aggregates calls to one tool at either Execution, Scope, or
// Session level.
type ToolUsage struct {
	Name       string
	Calls      int
	Failures   int
	DurationMs int64
}

// ConversationNode is one selectable event in an Execution timeline. Prompt
// nodes carry the complete recorded model input; no message-dedup heuristic is
// used to reconstruct context after compaction.
type ConversationNode struct {
	ID               string
	Kind             string
	Label            string
	Preview          string
	Text             string
	Messages         []DisplayMessage
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
	CacheReadTokens  int
	CacheWriteTokens int
	PromptDelta      int
	MessageDelta     int
	OK               bool
	HasResult        bool
}

func finalizeReview(scope *ReviewScope) {
	scope.Stage = classifyReview(scope)
	scope.Tools = nil
	scope.Metrics = ReviewMetrics{}
	toolIdx := map[string]*ToolUsage{}

	for _, execution := range scope.Executions {
		finalizeExecution(execution)
		addMetrics(&scope.Metrics, execution.Metrics)
		for _, tool := range execution.Tools {
			mergeTool(toolIdx, tool)
		}
		if execution.Status == "completed" {
			scope.ExecutionDone++
		} else {
			scope.ExecutionMissed++
		}
	}

	// Direct plan/re-location calls are Scope support work rather than AgentGo
	// Executions. Keep their cost in the Scope/session rollup without inventing
	// a fake Execution lifecycle for them.
	for _, card := range scope.Calls {
		if card.ExecutionID == "" {
			addCardMetrics(&scope.Metrics, card)
		}
	}

	scope.Tools = sortedTools(toolIdx)
	if len(scope.Executions) > 0 && scope.ExecutionMissed == 0 {
		scope.Status = "completed"
	} else {
		scope.Status = "incomplete"
	}
	if scope.Stage == Review1Stage {
		scope.FileReads = analyzeFileReads(scope)
	}
}

func finalizeExecution(execution *ReviewExecution) {
	toolIdx := map[string]*ToolUsage{}
	previousPrompt := 0
	previousMessages := 0
	for _, card := range execution.Calls {
		addCardMetrics(&execution.Metrics, card)
		if card.TaskType == execution.TaskType {
			card.TurnNo = len(execution.Turns) + 1
			card.PromptDelta = card.PromptTokens - previousPrompt
			card.MessageDelta = len(card.Request) - previousMessages
			previousPrompt = card.PromptTokens
			previousMessages = len(card.Request)
			execution.Turns = append(execution.Turns, card)
		}
		for _, call := range card.ToolCalls {
			mergeTool(toolIdx, ToolUsage{
				Name: call.Name, Calls: 1, DurationMs: call.DurationMs,
				Failures: boolInt(call.HasResult && !call.Ok),
			})
		}
	}
	execution.Metrics.TurnCount = len(execution.Turns)
	execution.Conversation = buildConversation(execution.ID, execution.Turns)
	execution.Tools = sortedTools(toolIdx)
	for _, tool := range execution.Tools {
		execution.Metrics.ToolCalls += tool.Calls
		execution.Metrics.ToolFailures += tool.Failures
	}
	execution.Metrics.ElapsedSec = float64(execution.DurationMs) / 1000
	execution.Status = "incomplete"
	if execution.Outcome == "completed" {
		execution.Status = "completed"
	}
}

func addCardMetrics(metrics *ReviewMetrics, card *TaskCard) {
	metrics.LLMCalls++
	metrics.LLMDurationMs += card.DurationMs
	metrics.PromptTokens += card.PromptTokens
	metrics.CompletionTokens += card.CompletionTokens
	metrics.CacheReadTokens += card.CacheReadTokens
	metrics.CacheWriteTokens += card.CacheWriteTokens
	if card.PromptTokens > metrics.MaxPromptTokens {
		metrics.MaxPromptTokens = card.PromptTokens
	}
}

func addMetrics(total *ReviewMetrics, value ReviewMetrics) {
	total.ElapsedSec += value.ElapsedSec
	total.LLMDurationMs += value.LLMDurationMs
	total.PromptTokens += value.PromptTokens
	total.CompletionTokens += value.CompletionTokens
	total.CacheReadTokens += value.CacheReadTokens
	total.CacheWriteTokens += value.CacheWriteTokens
	total.LLMCalls += value.LLMCalls
	total.TurnCount += value.TurnCount
	total.ToolCalls += value.ToolCalls
	total.ToolFailures += value.ToolFailures
	if value.MaxPromptTokens > total.MaxPromptTokens {
		total.MaxPromptTokens = value.MaxPromptTokens
	}
}

func buildConversation(executionID string, turns []*TaskCard) []ConversationNode {
	var nodes []ConversationNode
	nextID := 1
	appendNode := func(node ConversationNode) {
		node.ID = fmt.Sprintf("execution-%s-%d", executionID, nextID)
		nextID++
		nodes = append(nodes, node)
	}

	for _, turn := range turns {
		appendNode(ConversationNode{
			Kind: "prompt", Label: fmt.Sprintf("Prompt Snapshot · Turn %d", turn.TurnNo),
			Preview: fmt.Sprintf("%d messages", len(turn.Request)), Messages: turn.Request,
			TurnNo: turn.TurnNo, PromptTokens: turn.PromptTokens,
			CacheReadTokens: turn.CacheReadTokens, CacheWriteTokens: turn.CacheWriteTokens,
			PromptDelta: turn.PromptDelta, MessageDelta: turn.MessageDelta,
		})

		preview := firstLine(turn.ResponseContent)
		if preview == "" {
			preview = firstLine(turn.Reasoning)
		}
		if preview == "" && len(turn.ToolCalls) > 0 {
			preview = fmt.Sprintf("requested %d tool call(s)", len(turn.ToolCalls))
		}
		appendNode(ConversationNode{
			Kind: "assistant", Label: fmt.Sprintf("Assistant · Turn %d", turn.TurnNo), Preview: preview,
			Text: turn.ResponseContent, Reasoning: turn.Reasoning, Model: turn.Model,
			StopReason: turn.StopReason, Error: turn.Error, TurnNo: turn.TurnNo,
			DurationMs: turn.DurationMs, PromptTokens: turn.PromptTokens,
			CompletionTokens: turn.CompletionTokens,
		})

		for _, call := range turn.ToolCalls {
			appendNode(ConversationNode{
				Kind: "tool", Label: call.Name, Preview: toolTarget(call.Arguments),
				Arguments: call.Arguments, Result: call.Result, ToolCallID: call.ID,
				TurnNo: turn.TurnNo, Depth: 1, DurationMs: call.DurationMs,
				OK: call.Ok, HasResult: call.HasResult,
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
		if reads, ok := args["reads"].([]any); ok {
			var paths []string
			for _, value := range reads {
				item, _ := value.(map[string]any)
				if filePath, _ := item["file_path"].(string); filePath != "" {
					paths = append(paths, filePath)
				}
			}
			if len(paths) > 0 {
				return strings.Join(paths, ", ")
			}
		}
		if searches, ok := args["searches"].([]any); ok {
			var queries []string
			for _, value := range searches {
				item, _ := value.(map[string]any)
				if query, _ := item["query"].(string); query != "" {
					queries = append(queries, query)
				}
			}
			if len(queries) > 0 {
				return strings.Join(queries, ", ")
			}
		}
		for _, key := range []string{"file_path", "query", "query_name", "path"} {
			if value, ok := args[key].(string); ok && value != "" {
				return value
			}
		}
	}
	return firstLine(arguments)
}

func classifyReview(scope *ReviewScope) ReviewStage {
	if scope.Kind == "unit" {
		return Review1Stage
	}
	if scope.Kind == "lane" && scope.Scope == "hypothesis_review" {
		return Review2Stage
	}
	if scope.Kind == "file" || scope.Scope == "scan" {
		return ScanStage
	}
	return OtherStage
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

func mergeTool(index map[string]*ToolUsage, value ToolUsage) {
	tool := index[value.Name]
	if tool == nil {
		tool = &ToolUsage{Name: value.Name}
		index[value.Name] = tool
	}
	tool.Calls += value.Calls
	tool.Failures += value.Failures
	tool.DurationMs += value.DurationMs
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
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
func (vs *ViewSession) Review(id string) *ReviewScope {
	for _, review := range vs.Reviews {
		if review.ID == id {
			return review
		}
	}
	return nil
}
