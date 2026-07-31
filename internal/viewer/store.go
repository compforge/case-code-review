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

		subEntries, err := os.ReadDir(repoDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if strings.HasSuffix(se.Name(), ".jsonl") {
				info.SessionCount++
				if fi, err := se.Info(); err == nil {
					if fi.ModTime().After(info.LastModified) {
						info.LastModified = fi.ModTime()
					}
				}
			}
		}
		if info.SessionCount > 0 {
			repos = append(repos, info)
		}
	}

	sort.Slice(repos, func(i, j int) bool {
		return repos[i].LastModified.After(repos[j].LastModified)
	})
	return repos, nil
}

// SessionSummary is built from session_start and session_end records.
type SessionSummary struct {
	SessionID     string
	Timestamp     time.Time
	CWD           string
	GitBranch     string
	Model         string
	ReviewMode    string
	DiffFrom      string
	DiffTo        string
	DiffCommit    string
	FilesReviewed []string
	DurationSec   float64
	FileCount     int
	LLMFailures   int
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
	f, err := os.Open(path)
	if err != nil {
		return SessionSummary{}, err
	}
	defer f.Close()

	var summary SessionSummary
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var lastLine []byte
	for scanner.Scan() {
		line := scanner.Bytes()
		lastLine = append([]byte(nil), line...)

		if summary.Timestamp.IsZero() {
			var rec map[string]any
			if err := json.Unmarshal(line, &rec); err != nil {
				continue
			}
			if ts, ok := rec["timestamp"].(string); ok {
				summary.Timestamp, _ = time.Parse(time.RFC3339, ts)
			}
			if cwd, ok := rec["cwd"].(string); ok {
				summary.CWD = cwd
			}
			if branch, ok := rec["gitBranch"].(string); ok {
				summary.GitBranch = branch
			}
			if model, ok := rec["model"].(string); ok {
				summary.Model = model
			}
			if rm, ok := rec["reviewMode"].(string); ok {
				summary.ReviewMode = rm
			}
			if v, ok := rec["diffFrom"].(string); ok {
				summary.DiffFrom = v
			}
			if v, ok := rec["diffTo"].(string); ok {
				summary.DiffTo = v
			}
			if v, ok := rec["diffCommit"].(string); ok {
				summary.DiffCommit = v
			}
		}
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
	TokenUsage    TokenUsageSummary
	ToolUsage     []ToolUsage
	SystemPrompts []SystemPrompt // distinct system prompts, deduped by content
	Artifacts     []ReviewArtifact
	Reviews       []*ReviewRun
}

// ReviewArtifact is a domain-owned intermediate decision rendered without
// teaching the viewer the evolving Hypothesis or Assessment schemas.
type ReviewArtifact struct {
	Kind string
	Data string
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
	// Request holds the complete recorded message list sent in this call.
	Request          []DisplayMessage
	TaskType         TaskType
	RequestNo        int
	TurnNo           int
	PromptDelta      int
	MessageDelta     int
	ResponseContent  string
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

// ToolCallInfo summarizes a single tool call.
type ToolCallInfo struct {
	Name       string
	Arguments  string
	Result     string
	Ok         bool
	DurationMs int64
}

// LoadSession fully parses a JSONL file into a ViewSession.
func LoadSession(root, encodedRepo, sessionID string) (*ViewSession, error) {
	path := filepath.Join(root, encodedRepo, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session file: %w", err)
	}
	defer f.Close()

	vs := &ViewSession{Reviews: make([]*ReviewRun, 0)}
	reviewIndex := make(map[string]*ReviewRun)
	sysIndex := make(map[string]int) // system prompt text -> index in vs.SystemPrompts

	// groupFor resolves the ReviewRun a record belongs to, keyed by scope_id.
	groupFor := func(rec map[string]any) *ReviewRun {
		key, _ := rec["scope_id"].(string)
		run := reviewIndex[key]
		if run == nil {
			kind, _ := rec["kind"].(string)
			scope, _ := rec["scope"].(string)
			fp, _ := rec["filePath"].(string)
			run = &ReviewRun{ID: key, Kind: kind, Scope: scope, Paths: stringList(rec["paths"]), FilePath: fp, Tasks: make(map[TaskType][]*TaskCard)}
			reviewIndex[key] = run
			vs.Reviews = append(vs.Reviews, run)
		}
		return run
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 10*1024*1024)

	for scanner.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue // skip malformed lines
		}
		typ, _ := rec["type"].(string)
		if scopeID, _ := rec["scope_id"].(string); scopeID != "" {
			groupFor(rec).observeTimestamp(rec["timestamp"])
		}

		switch typ {
		case "session_start":
			if ts, ok := rec["timestamp"].(string); ok {
				vs.Summary.Timestamp, _ = time.Parse(time.RFC3339, ts)
			}
			if cwd, ok := rec["cwd"].(string); ok {
				vs.Summary.CWD = cwd
			}
			if branch, ok := rec["gitBranch"].(string); ok {
				vs.Summary.GitBranch = branch
			}
			if model, ok := rec["model"].(string); ok {
				vs.Summary.Model = model
			}
			if rm, ok := rec["reviewMode"].(string); ok {
				vs.Summary.ReviewMode = rm
			}
			if v, ok := rec["diffFrom"].(string); ok {
				vs.Summary.DiffFrom = v
			}
			if v, ok := rec["diffTo"].(string); ok {
				vs.Summary.DiffTo = v
			}
			if v, ok := rec["diffCommit"].(string); ok {
				vs.Summary.DiffCommit = v
			}

		case "llm_request":
			tt, _ := rec["taskType"].(string)
			reqNo := 0
			if n, ok := rec["request_no"].(float64); ok {
				reqNo = int(n)
			}
			var reqMsgs []DisplayMessage
			for _, m := range extractMessages(rec["messages"]) {
				if m.Role == "system" {
					registerSystemPrompt(vs, sysIndex, TaskType(tt), m.Text)
				}
				reqMsgs = append(reqMsgs, m)
			}

			tc := &TaskCard{Request: reqMsgs, TaskType: TaskType(tt), RequestNo: reqNo}
			fg := groupFor(rec)
			fg.Tasks[TaskType(tt)] = append(fg.Tasks[TaskType(tt)], tc)
			fg.Calls = append(fg.Calls, tc)

		case "artifact":
			kind, _ := rec["artifact_kind"].(string)
			if kind == "" {
				continue
			}
			data, err := json.MarshalIndent(rec["data"], "", "  ")
			if err != nil {
				continue
			}
			vs.Artifacts = append(vs.Artifacts, ReviewArtifact{Kind: kind, Data: string(data)})

		case "llm_response":
			content, _ := rec["content"].(string)
			durationMs := int64(0)
			if d, ok := rec["duration_ms"].(float64); ok {
				durationMs = int64(d)
			}
			model, _ := rec["model"].(string)
			errStr, _ := rec["error"].(string)

			promptTok := 0
			completionTok := 0
			cacheReadTok := 0
			cacheWriteTok := 0
			if usage, ok := rec["usage"].(map[string]any); ok {
				if v, ok := usage["prompt_tokens"].(float64); ok {
					promptTok = int(v)
				}
				if v, ok := usage["completion_tokens"].(float64); ok {
					completionTok = int(v)
				}
				if v, ok := usage["cache_read_tokens"].(float64); ok {
					cacheReadTok = int(v)
				}
				if v, ok := usage["cache_write_tokens"].(float64); ok {
					cacheWriteTok = int(v)
				}
			}

			tt, _ := rec["taskType"].(string)
			fg := groupFor(rec)
			if fg != nil {
				cards := fg.Tasks[TaskType(tt)]
				if len(cards) > 0 && !cards[len(cards)-1].HasResponse {
					card := cards[len(cards)-1]
					card.ResponseContent = content
					card.HasResponse = true
					card.DurationMs = durationMs
					card.Model = model
					card.Error = errStr
					card.PromptTokens = promptTok
					card.CompletionTokens = completionTok
					card.CacheReadTokens = cacheReadTok
					card.CacheWriteTokens = cacheWriteTok
				}
			}

			// Also attach tool_calls to the same card
			if tcs, ok := rec["tool_calls"].([]any); ok && fg != nil {
				tt, _ := rec["taskType"].(string)
				cards := fg.Tasks[TaskType(tt)]
				if len(cards) > 0 {
					card := cards[len(cards)-1]
					for _, tc := range tcs {
						if tm, ok := tc.(map[string]any); ok {
							name, _ := tm["name"].(string)
							args, _ := tm["arguments"].(string)
							info := ToolCallInfo{Name: name, Arguments: args}
							if name == "task_done" {
								info.Ok = true
							}
							card.ToolCalls = append(card.ToolCalls, info)
						}
					}
				}
			}

		case "llm_error":
			tt, _ := rec["taskType"].(string)
			errStr, _ := rec["error"].(string)
			durationMs := int64(0)
			if d, ok := rec["duration_ms"].(float64); ok {
				durationMs = int64(d)
			}

			fg := groupFor(rec)
			if fg != nil {
				cards := fg.Tasks[TaskType(tt)]
				if len(cards) > 0 && !cards[len(cards)-1].HasResponse && cards[len(cards)-1].Error == "" {
					card := cards[len(cards)-1]
					card.Error = errStr
					card.DurationMs = durationMs
				}
			}

		case "tool_call":
			result, _ := rec["result"].(string)
			okVal := true
			if b, hasOk := rec["ok"].(bool); hasOk {
				okVal = b
			}
			tt, _ := rec["taskType"].(string)
			durationMs := int64(0)
			if d, ok2 := rec["duration_ms"].(float64); ok2 {
				durationMs = int64(d)
			}
			fg := groupFor(rec)
			if fg != nil {
				cards := fg.Tasks[TaskType(tt)]
				if len(cards) > 0 {
					card := cards[len(cards)-1]
					for ti := range card.ToolCalls {
						if card.ToolCalls[ti].Result == "" && !card.ToolCalls[ti].Ok {
							card.ToolCalls[ti].Result = result
							card.ToolCalls[ti].Ok = okVal
							card.ToolCalls[ti].DurationMs = durationMs
							break
						}
					}
				}
			}

		case "session_end":
			if dur, ok := rec["duration_seconds"].(float64); ok {
				vs.Summary.DurationSec = dur
			}
			if files, ok := rec["files_reviewed"].([]any); ok {
				vs.Summary.FilesReviewed = make([]string, 0, len(files))
				for _, fv := range files {
					if s, ok2 := fv.(string); ok2 {
						vs.Summary.FilesReviewed = append(vs.Summary.FilesReviewed, s)
					}
				}
			}
			vs.Summary.FileCount = len(vs.Summary.FilesReviewed)
			if f, ok := rec["llm_failures"].(float64); ok {
				vs.Summary.LLMFailures = int(f)
			}
		}
	}

	// Build the same read model at both levels: the session overview is a rollup
	// of Review 1/2 runs, while each run preserves its ordered prompt timeline.
	fileIdx := make(map[string]*FileTokenUsage)
	fileOrder := make([]string, 0)
	sessionTools := map[string]*ToolUsage{}
	for _, run := range vs.Reviews {
		finalizeReview(run)

		rollupKey := run.FilePath
		if run.Kind == "run" {
			rollupKey = "(run-level)"
		}
		ft := fileIdx[rollupKey]
		if ft == nil {
			ft = &FileTokenUsage{FilePath: rollupKey}
			fileIdx[rollupKey] = ft
			fileOrder = append(fileOrder, rollupKey)
		}
		vs.TokenUsage.TotalPromptTokens += run.Metrics.PromptTokens
		vs.TokenUsage.TotalCompletionTokens += run.Metrics.CompletionTokens
		vs.TokenUsage.TotalCacheReadTokens += run.Metrics.CacheReadTokens
		vs.TokenUsage.TotalCacheWriteTokens += run.Metrics.CacheWriteTokens
		vs.TokenUsage.RequestCount += run.Metrics.LLMCalls
		ft.PromptTokens += run.Metrics.PromptTokens
		ft.CompletionTokens += run.Metrics.CompletionTokens
		ft.CacheReadTokens += run.Metrics.CacheReadTokens
		ft.CacheWriteTokens += run.Metrics.CacheWriteTokens

		for _, tool := range run.Tools {
			total := sessionTools[tool.Name]
			if total == nil {
				total = &ToolUsage{Name: tool.Name}
				sessionTools[tool.Name] = total
			}
			total.Calls += tool.Calls
			total.Failures += tool.Failures
			total.DurationMs += tool.DurationMs
		}
	}
	fileBreakdown := make([]FileTokenUsage, 0, len(fileOrder))
	for _, p := range fileOrder {
		fileBreakdown = append(fileBreakdown, *fileIdx[p])
	}
	sort.Slice(fileBreakdown, func(i, j int) bool {
		return fileBreakdown[i].PromptTokens+fileBreakdown[i].CompletionTokens > fileBreakdown[j].PromptTokens+fileBreakdown[j].CompletionTokens
	})
	vs.TokenUsage.FileTokenBreakdown = fileBreakdown
	vs.ToolUsage = sortedTools(sessionTools)

	// Review 1 first, then Review 2 and non-diff scan passes; stable by id
	// within each stage so overview links do not jump between reloads.
	sort.SliceStable(vs.Reviews, func(i, j int) bool {
		a, b := vs.Reviews[i], vs.Reviews[j]
		if a.Stage != b.Stage {
			return reviewStageRank(a.Stage) < reviewStageRank(b.Stage)
		}
		return a.ID < b.ID
	})

	vs.Summary.SessionID = sessionID
	return vs, scanner.Err()
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
