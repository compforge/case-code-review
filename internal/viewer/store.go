// Package viewer provides a read-only WebUI for browsing session records
// produced by case-code-review runs. It scans JSONL files under
// $HOME/.casecodereview/sessions/, parses them, and exposes structured data.
package viewer

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness/session"
)

// SessionsRoot returns the root directory where session JSONL files are stored.
func SessionsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".casecodereview", "sessions"), nil
}

// RepoInfo represents a discovered repository from the sessions directory.
type RepoInfo struct {
	EncodedPath  string // encoded directory name on disk
	SessionCount int
	LastModified time.Time
	LatestBizID  string
}

// DiscoverRepos walks the sessions root and returns one entry per subdirectory.
func DiscoverRepos(root string) ([]RepoInfo, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions dir: %w", err)
	}

	var repos []RepoInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		repoDir := filepath.Join(root, e.Name())
		info := RepoInfo{EncodedPath: e.Name()}
		latestSessionPath := ""

		subEntries, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if !strings.HasSuffix(se.Name(), ".jsonl") {
				continue
			}
			path := filepath.Join(repoDir, se.Name())
			if _, err := readSessionStart(path); err != nil {
				continue
			}
			info.SessionCount++
			if fi, err := se.Info(); err == nil && fi.ModTime().After(info.LastModified) {
				info.LastModified = fi.ModTime()
				latestSessionPath = path
			}
		}
		if info.SessionCount > 0 {
			info.LatestBizID = readSessionBizID(latestSessionPath)
			repos = append(repos, info)
		}
	}

	sort.Slice(repos, func(i, j int) bool {
		return repos[i].LastModified.After(repos[j].LastModified)
	})
	return repos, nil
}

func readSessionBizID(path string) string {
	summary, err := readSessionStart(path)
	if err != nil {
		return ""
	}
	return summary.BizID
}

func readSessionStart(path string) (SessionSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return SessionSummary{}, fmt.Errorf("session is empty")
	}
	var record map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		return SessionSummary{}, err
	}
	if stringValue(record["type"]) != "session_start" {
		return SessionSummary{}, fmt.Errorf("first record is not session_start")
	}
	version := intValue(record["schema_version"])
	if version != session.SchemaVersion {
		return SessionSummary{}, fmt.Errorf("unsupported session schema %d", version)
	}
	summary := SessionSummary{
		SchemaVersion: version,
		CWD:           stringValue(record["cwd"]), GitBranch: stringValue(record["gitBranch"]),
		Model: stringValue(record["model"]), ReviewMode: stringValue(record["reviewMode"]),
		DiffFrom: stringValue(record["diffFrom"]), DiffTo: stringValue(record["diffTo"]),
		DiffCommit: stringValue(record["diffCommit"]), BizID: stringValue(record["biz_id"]),
	}
	if raw := stringValue(record["timestamp"]); raw != "" {
		summary.Timestamp, _ = time.Parse(time.RFC3339, raw)
	}
	return summary, nil
}

// SessionSummary is built from session_start and session_end records.
type SessionSummary struct {
	SchemaVersion  int
	SessionID      string
	Timestamp      time.Time
	CWD            string
	GitBranch      string
	Model          string
	ReviewMode     string
	DiffFrom       string
	DiffTo         string
	DiffCommit     string
	BizID          string
	FilesReviewed  []string
	DiffFileCount  int
	DiffInsertions int
	DiffDeletions  int
	HasDiffStats   bool
	DurationSec    float64
	FileCount      int
	LLMFailures    int
}

// ListSessions returns lightweight summaries for all sessions in a repo subdir.
func ListSessions(root, encodedRepo string) ([]SessionSummary, error) {
	repoDir := filepath.Join(root, encodedRepo)
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		return nil, fmt.Errorf("read repo dir: %w", err)
	}

	var summaries []SessionSummary
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		sessionID := strings.TrimSuffix(e.Name(), ".jsonl")
		s, err := peekSession(filepath.Join(repoDir, e.Name()))
		if err != nil {
			continue // skip unreadable files
		}
		s.SessionID = sessionID
		summaries = append(summaries, s)
	}

	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].Timestamp.After(summaries[j].Timestamp)
	})
	return summaries, nil
}

// peekSession reads only the first and last record of a JSONL file.
func peekSession(path string) (SessionSummary, error) {
	summary, err := readSessionStart(path)
	if err != nil {
		return SessionSummary{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var lastLine []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		lastLine = append([]byte(nil), line...)
	}

	if len(lastLine) > 0 {
		var rec map[string]any
		if err := json.Unmarshal(lastLine, &rec); err == nil {
			if typ, _ := rec["type"].(string); typ == "session_end" {
				if dur, ok := rec["duration_seconds"].(float64); ok {
					summary.DurationSec = dur
				}
				if files, ok := rec["files_reviewed"].([]any); ok {
					summary.FilesReviewed = make([]string, 0, len(files))
					for _, fv := range files {
						if s, ok := fv.(string); ok {
							summary.FilesReviewed = append(summary.FilesReviewed, s)
						}
					}
				}
				if n, ok := rec["diff_files"].(float64); ok {
					summary.DiffFileCount = int(n)
					summary.HasDiffStats = true
				}
				if n, ok := rec["diff_insertions"].(float64); ok {
					summary.DiffInsertions = int(n)
				}
				if n, ok := rec["diff_deletions"].(float64); ok {
					summary.DiffDeletions = int(n)
				}
				if f, ok := rec["llm_failures"].(float64); ok {
					summary.LLMFailures = int(f)
				}
			}
		}
	}
	summary.FileCount = len(summary.FilesReviewed)
	return summary, scanner.Err()
}

// ViewSession holds fully parsed records for one session.
type ViewSession struct {
	Summary       SessionSummary
	Diagnostics   SessionDiagnostics
	TokenUsage    TokenUsageSummary
	Compaction    CompactionSummary
	ToolUsage     []ToolUsage
	SystemPrompts []SystemPrompt // distinct system prompts, deduped by content
	Artifacts     []ReviewArtifact
	Reviews       []*ReviewScope
}

// CompactionSummary aggregates explicit ContextManager rewrites. Counting
// events avoids inferring compaction from prompt-size deltas.
type CompactionSummary struct {
	Count      int
	Summarized int
}

// ReviewArtifact is a domain-owned intermediate decision rendered without
// teaching the viewer the evolving Hypothesis or Assessment schemas.
type ReviewArtifact struct {
	Kind         string
	Data         string
	Status       string // assessment only: current | superseded
	OriginUnit   string
	LaneID       string
	HypothesisID string
}

// DisplayMessage is one message of an LLM request with its text content
// extracted for display (handles both plain-string and content-block formats).
type DisplayMessage struct {
	Role string
	Text string
}

// SystemPrompt is a distinct system prompt seen in the session. System prompts
// are static per task type and repeat across every file/round, so the viewer
// dedupes them by content and surfaces them once at the session level rather
// than burying a copy inside every request card.
type SystemPrompt struct {
	TaskTypes []TaskType // task types this exact prompt was used for
	Text      string
}

// TokenUsageSummary aggregates token counts across the session.
type TokenUsageSummary struct {
	TotalPromptTokens     int
	TotalCompletionTokens int
	TotalCacheReadTokens  int
	TotalCacheWriteTokens int
	RequestCount          int
	FileTokenBreakdown    []FileTokenUsage
}

// FileTokenUsage tracks token totals for a single file within a session.
type FileTokenUsage struct {
	FilePath         string
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
}

// TaskType mirrors session.TaskType.
type TaskType string

const (
	PlanTask              TaskType = "plan_task"
	MainTask              TaskType = "main_task"
	MemoryCompressionTask TaskType = "memory_compression_task"
	ReLocationTask        TaskType = "re_location_task"
	HypothesisReviewTask  TaskType = "hypothesis_review_task"
)

// TaskCard links an LLM request with its response and tool calls.
type TaskCard struct {
	ExecutionID string
	Sequence    int
	// Request holds the complete recorded message list sent in this call.
	Request          []DisplayMessage
	TaskType         TaskType
	RequestNo        int
	TurnNo           int
	PromptDelta      int
	MessageDelta     int
	ResponseContent  string
	Reasoning        string
	StopReason       string
	ToolCalls        []ToolCallInfo
	DurationMs       int64
	Error            string
	Model            string
	HasResponse      bool
	PromptTokens     int
	CompletionTokens int
	CacheReadTokens  int
	CacheWriteTokens int
}

// ContextCompaction is one persisted rewrite positioned in the execution
// conversation by its JSONL sequence.
type ContextCompaction struct {
	Sequence       int
	Reason         string
	Committed      bool
	TokensBefore   int
	TokensAfter    int
	MessagesBefore int
	MessagesAfter  int
	Summarized     bool
}

// ToolCallInfo summarizes a single tool call.
type ToolCallInfo struct {
	ID         string
	Name       string
	Arguments  string
	Result     string
	Ok         bool
	HasResult  bool
	DurationMs int64
}

// LoadSession fully parses one current-schema JSONL file into the Viewer read model.
func LoadSession(root, encodedRepo, sessionID string) (*ViewSession, error) {
	path := filepath.Join(root, encodedRepo, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	vs := &ViewSession{Reviews: make([]*ReviewScope, 0)}
	scopeIndex := make(map[string]*ReviewScope)
	executionIndex := make(map[string]*ReviewExecution)
	sysIndex := make(map[string]int)

	scopeFor := func(rec map[string]any) *ReviewScope {
		key, _ := rec["scope_id"].(string)
		if key == "" {
			return nil
		}
		scope := scopeIndex[key]
		if scope == nil {
			kind, _ := rec["kind"].(string)
			scopeType, _ := rec["scope"].(string)
			filePath, _ := rec["filePath"].(string)
			scope = &ReviewScope{
				ID: key, Kind: kind, Scope: scopeType, Paths: stringList(rec["paths"]),
				FilePath: filePath, Tasks: make(map[TaskType][]*TaskCard),
			}
			scopeIndex[key] = scope
			vs.Reviews = append(vs.Reviews, scope)
		} else {
			scope.Paths = mergeStrings(scope.Paths, stringList(rec["paths"]))
		}
		return scope
	}

	executionFor := func(rec map[string]any) (*ReviewScope, *ReviewExecution) {
		scope := scopeFor(rec)
		executionID, _ := rec["execution_id"].(string)
		if scope == nil || executionID == "" {
			return scope, nil
		}
		key := scope.ID + "\x00" + executionID
		execution := executionIndex[key]
		if execution == nil {
			execution = &ReviewExecution{ID: executionID, Tasks: make(map[TaskType][]*TaskCard)}
			executionIndex[key] = execution
			scope.Executions = append(scope.Executions, execution)
		}
		return scope, execution
	}

	cardsFor := func(scope *ReviewScope, execution *ReviewExecution, taskType TaskType) []*TaskCard {
		if execution != nil {
			return execution.Tasks[taskType]
		}
		if scope != nil {
			return scope.Tasks[taskType]
		}
		return nil
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	latestAssessment := make(map[string]int)
	signals := newSessionSignals()
	hasCurrentSchema := false
	sequence := 0
	for scanner.Scan() {
		sequence++
		var rec map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("decode session record: %w", err)
		}
		typ, _ := rec["type"].(string)

		switch typ {
		case "session_start":
			version := intValue(rec["schema_version"])
			if version != session.SchemaVersion {
				return nil, fmt.Errorf("unsupported session schema %d; viewer requires %d", version, session.SchemaVersion)
			}
			hasCurrentSchema = true
			vs.Summary.SchemaVersion = version
			if ts, ok := rec["timestamp"].(string); ok {
				vs.Summary.Timestamp, _ = time.Parse(time.RFC3339, ts)
			}
			vs.Summary.CWD, _ = rec["cwd"].(string)
			vs.Summary.GitBranch, _ = rec["gitBranch"].(string)
			vs.Summary.Model, _ = rec["model"].(string)
			vs.Summary.ReviewMode, _ = rec["reviewMode"].(string)
			vs.Summary.DiffFrom, _ = rec["diffFrom"].(string)
			vs.Summary.DiffTo, _ = rec["diffTo"].(string)
			vs.Summary.DiffCommit, _ = rec["diffCommit"].(string)
			vs.Summary.BizID, _ = rec["biz_id"].(string)

		case "llm_request":
			taskType := TaskType(stringValue(rec["taskType"]))
			var request []DisplayMessage
			for _, message := range extractMessages(rec["messages"]) {
				if message.Role == "system" {
					registerSystemPrompt(vs, sysIndex, taskType, message.Text)
				}
				request = append(request, message)
			}
			scope, execution := executionFor(rec)
			if scope == nil {
				continue
			}
			card := &TaskCard{
				ExecutionID: stringValue(rec["execution_id"]), Request: request,
				TaskType: taskType, RequestNo: intValue(rec["request_no"]), Sequence: sequence,
			}
			scope.Tasks[taskType] = append(scope.Tasks[taskType], card)
			scope.Calls = append(scope.Calls, card)
			if execution != nil {
				execution.Tasks[taskType] = append(execution.Tasks[taskType], card)
				execution.Calls = append(execution.Calls, card)
			}

		case "llm_response":
			taskType := TaskType(stringValue(rec["taskType"]))
			scope, execution := executionFor(rec)
			cards := cardsFor(scope, execution, taskType)
			if len(cards) == 0 {
				continue
			}
			card := cards[len(cards)-1]
			if card.HasResponse {
				continue
			}
			card.ResponseContent = stringValue(rec["content"])
			card.Reasoning = stringValue(rec["reasoning"])
			card.StopReason = stringValue(rec["stop_reason"])
			card.DurationMs = int64(intValue(rec["duration_ms"]))
			card.Model = stringValue(rec["model"])
			card.Error = stringValue(rec["error"])
			card.HasResponse = true
			if usage, ok := rec["usage"].(map[string]any); ok {
				card.PromptTokens = intValue(usage["prompt_tokens"])
				card.CompletionTokens = intValue(usage["completion_tokens"])
				card.CacheReadTokens = intValue(usage["cache_read_tokens"])
				card.CacheWriteTokens = intValue(usage["cache_write_tokens"])
			}
			if calls, ok := rec["tool_calls"].([]any); ok {
				for _, raw := range calls {
					call, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					card.ToolCalls = append(card.ToolCalls, ToolCallInfo{
						ID: stringValue(call["id"]), Name: stringValue(call["name"]),
						Arguments: stringValue(call["arguments"]),
					})
				}
			}

		case "llm_error":
			taskType := TaskType(stringValue(rec["taskType"]))
			scope, execution := executionFor(rec)
			cards := cardsFor(scope, execution, taskType)
			if len(cards) == 0 {
				continue
			}
			card := cards[len(cards)-1]
			card.Error = stringValue(rec["error"])
			card.DurationMs = int64(intValue(rec["duration_ms"]))

		case "tool_call":
			taskType := TaskType(stringValue(rec["taskType"]))
			scope, execution := executionFor(rec)
			cards := cardsFor(scope, execution, taskType)
			if len(cards) == 0 {
				continue
			}
			card := cards[len(cards)-1]
			toolCallID := stringValue(rec["tool_call_id"])
			toolName := stringValue(rec["tool_name"])
			match := -1
			for i := range card.ToolCalls {
				call := &card.ToolCalls[i]
				if toolCallID != "" && call.ID == toolCallID {
					match = i
					break
				}
				if toolCallID == "" && match == -1 && !call.HasResult && call.Name == toolName {
					match = i
				}
			}
			if match >= 0 {
				card.ToolCalls[match].Result = stringValue(rec["result"])
				card.ToolCalls[match].Ok = boolValue(rec["ok"], true)
				card.ToolCalls[match].HasResult = true
				card.ToolCalls[match].DurationMs = int64(intValue(rec["duration_ms"]))
			}

		case "context_compacted":
			_, execution := executionFor(rec)
			if execution == nil {
				return nil, fmt.Errorf("context_compacted missing scope or execution_id")
			}
			compaction := ContextCompaction{
				Sequence: sequence, Reason: stringValue(rec["reason"]),
				Committed:    boolValue(rec["committed"], false),
				TokensBefore: intValue(rec["tokens_before"]), TokensAfter: intValue(rec["tokens_after"]),
				MessagesBefore: intValue(rec["messages_before"]), MessagesAfter: intValue(rec["messages_after"]),
				Summarized: boolValue(rec["summarized"], false),
			}
			execution.Compactions = append(execution.Compactions, compaction)
			vs.Compaction.Count++
			if compaction.Summarized {
				vs.Compaction.Summarized++
			}

		case "execution_end":
			_, execution := executionFor(rec)
			if execution == nil {
				return nil, fmt.Errorf("execution_end missing scope or execution_id")
			}
			execution.TaskType = TaskType(stringValue(rec["taskType"]))
			execution.Outcome = stringValue(rec["outcome"])
			execution.Reason = stringValue(rec["reason"])
			execution.DurationMs = int64(intValue(rec["duration_ms"]))
			execution.ToolCalls = intValue(rec["tool_calls"])
			execution.ToolErrors = intValue(rec["tool_errors"])

		case "artifact":
			kind := stringValue(rec["artifact_kind"])
			payload, _ := rec["data"].(map[string]any)
			if kind == "" || payload == nil {
				continue
			}
			signals.observeArtifact(kind, payload)
			data, err := json.MarshalIndent(payload, "", "  ")
			if err != nil {
				continue
			}
			artifact := ReviewArtifact{
				Kind: kind, Data: string(data), OriginUnit: stringValue(payload["origin_unit"]),
				LaneID: stringValue(payload["lane_id"]), HypothesisID: stringValue(payload["hypothesis_id"]),
			}
			if kind == "review_hypothesis" && artifact.HypothesisID == "" {
				artifact.HypothesisID = stringValue(payload["id"])
			}
			if kind == "review_assessment" && artifact.HypothesisID != "" {
				if previous, exists := latestAssessment[artifact.HypothesisID]; exists {
					vs.Artifacts[previous].Status = "superseded"
				}
				artifact.Status = "current"
				latestAssessment[artifact.HypothesisID] = len(vs.Artifacts)
			}
			vs.Artifacts = append(vs.Artifacts, artifact)

		case "debrief":
			scope := scopeFor(rec)
			if scope == nil {
				continue
			}
			if raw, ok := rec["source_preloads"]; ok {
				scope.SourcePreloads = stringList(raw)
				scope.HasSourcePreloads = true
			}
			if raw, ok := rec["context_paths"]; ok {
				scope.ContextPaths = stringMapLists(raw)
				scope.HasContextPaths = true
			}

		case "session_end":
			signals.hasSessionEnd = true
			if duration, ok := rec["duration_seconds"].(float64); ok {
				vs.Summary.DurationSec = duration
			}
			vs.Summary.FilesReviewed = stringList(rec["files_reviewed"])
			vs.Summary.DiffFileCount = intValue(rec["diff_files"])
			vs.Summary.HasDiffStats = rec["diff_files"] != nil
			vs.Summary.DiffInsertions = intValue(rec["diff_insertions"])
			vs.Summary.DiffDeletions = intValue(rec["diff_deletions"])
			vs.Summary.FileCount = len(vs.Summary.FilesReviewed)
			vs.Summary.LLMFailures = intValue(rec["llm_failures"])

		case "finding":
			signals.findings++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !hasCurrentSchema {
		return nil, fmt.Errorf("session_start with schema %d is required", session.SchemaVersion)
	}

	laneScopes := make(map[string]*ReviewScope)
	for _, scope := range vs.Reviews {
		if scope.Kind == "lane" {
			laneScopes[strings.TrimPrefix(scope.ID, "hypothesis_review:")] = scope
		}
	}
	for _, artifact := range vs.Artifacts {
		if artifact.OriginUnit != "" {
			if scope := scopeIndex[artifact.OriginUnit]; scope != nil {
				scope.Artifacts = append(scope.Artifacts, artifact)
			}
		}
		if artifact.LaneID != "" {
			if scope := laneScopes[artifact.LaneID]; scope != nil {
				scope.Artifacts = append(scope.Artifacts, artifact)
			}
		}
	}

	fileIdx := make(map[string]*FileTokenUsage)
	fileOrder := make([]string, 0)
	sessionTools := map[string]*ToolUsage{}
	supportedReviews := make([]*ReviewScope, 0, len(vs.Reviews))
	for _, scope := range vs.Reviews {
		scope.EncodedRepo = encodedRepo
		scope.SessionID = sessionID
		finalizeReview(scope)
		if scope.Stage == OtherStage {
			continue
		}
		supportedReviews = append(supportedReviews, scope)

		rollupKey := scope.FilePath
		if scope.Kind == "lane" {
			rollupKey = "(review-2 lanes)"
		}
		usage := fileIdx[rollupKey]
		if usage == nil {
			usage = &FileTokenUsage{FilePath: rollupKey}
			fileIdx[rollupKey] = usage
			fileOrder = append(fileOrder, rollupKey)
		}
		vs.TokenUsage.TotalPromptTokens += scope.Metrics.PromptTokens
		vs.TokenUsage.TotalCompletionTokens += scope.Metrics.CompletionTokens
		vs.TokenUsage.TotalCacheReadTokens += scope.Metrics.CacheReadTokens
		vs.TokenUsage.TotalCacheWriteTokens += scope.Metrics.CacheWriteTokens
		vs.TokenUsage.RequestCount += scope.Metrics.LLMCalls
		usage.PromptTokens += scope.Metrics.PromptTokens
		usage.CompletionTokens += scope.Metrics.CompletionTokens
		usage.CacheReadTokens += scope.Metrics.CacheReadTokens
		usage.CacheWriteTokens += scope.Metrics.CacheWriteTokens
		for _, tool := range scope.Tools {
			mergeTool(sessionTools, tool)
		}
	}
	vs.Reviews = supportedReviews
	for _, path := range fileOrder {
		vs.TokenUsage.FileTokenBreakdown = append(vs.TokenUsage.FileTokenBreakdown, *fileIdx[path])
	}
	sort.Slice(vs.TokenUsage.FileTokenBreakdown, func(i, j int) bool {
		a := vs.TokenUsage.FileTokenBreakdown[i]
		b := vs.TokenUsage.FileTokenBreakdown[j]
		return a.PromptTokens+a.CompletionTokens > b.PromptTokens+b.CompletionTokens
	})
	vs.ToolUsage = sortedTools(sessionTools)
	vs.Diagnostics = buildSessionDiagnostics(vs, signals)

	sort.SliceStable(vs.Reviews, func(i, j int) bool {
		a, b := vs.Reviews[i], vs.Reviews[j]
		if a.Stage != b.Stage {
			return reviewStageRank(a.Stage) < reviewStageRank(b.Stage)
		}
		return a.ID < b.ID
	})
	vs.Summary.SessionID = sessionID
	return vs, nil
}

// stringList coerces a JSON value (expected []any of strings) into []string.
func stringList(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func mergeStrings(left, right []string) []string {
	out := append([]string(nil), left...)
	for _, value := range right {
		if value != "" && !slices.Contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func intValue(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case int64:
		return int(number)
	default:
		return 0
	}
}

func boolValue(value any, fallback bool) bool {
	result, ok := value.(bool)
	if !ok {
		return fallback
	}
	return result
}

func stringMapLists(v any) map[string][]string {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string][]string, len(raw))
	for key, value := range raw {
		out[key] = stringList(value)
	}
	return out
}

// registerSystemPrompt dedupes a system prompt by exact text, recording which
// task types reused it. System prompts are static and repeat across every
// file/round, so the session keeps one entry per distinct text.
func registerSystemPrompt(vs *ViewSession, idx map[string]int, tt TaskType, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	if i, ok := idx[text]; ok {
		if !slices.Contains(vs.SystemPrompts[i].TaskTypes, tt) {
			vs.SystemPrompts[i].TaskTypes = append(vs.SystemPrompts[i].TaskTypes, tt)
		}
		return
	}
	idx[text] = len(vs.SystemPrompts)
	vs.SystemPrompts = append(vs.SystemPrompts, SystemPrompt{TaskTypes: []TaskType{tt}, Text: text})
}

// extractMessages turns the raw JSON `messages` array into display rows, pulling
// readable text out of each message's content (string or content blocks).
func extractMessages(raw any) []DisplayMessage {
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]DisplayMessage, 0, len(arr))
	for _, m := range arr {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := mm["role"].(string)
		out = append(out, DisplayMessage{Role: role, Text: extractContentText(mm["content"])})
	}
	return out
}

// extractContentText mirrors llm.Message.ExtractText for the map-shaped JSON the
// viewer reads back: content is either a plain string or an array of blocks.
func extractContentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var b strings.Builder
		for _, blk := range v {
			b.WriteString(extractBlockText(blk))
		}
		return b.String()
	default:
		return ""
	}
}

// extractBlockText pulls text from a single content block, recursing into nested
// blocks (e.g. a tool_result wrapping text blocks).
func extractBlockText(blk any) string {
	bm, ok := blk.(map[string]any)
	if !ok {
		return ""
	}
	if t, ok := bm["text"].(string); ok && t != "" {
		return t
	}
	if nested, ok := bm["content"].([]any); ok {
		var b strings.Builder
		for _, n := range nested {
			b.WriteString(extractBlockText(n))
		}
		return b.String()
	}
	return ""
}
