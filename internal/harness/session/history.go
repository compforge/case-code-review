// Package session provides a session history mechanism for collecting conversation
// records during code review task execution. It organizes records by review scope
// (a Unit, Review 2 Lane, or file-level scan — see ScopeSession) and request type.
package session

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/go-stdx/uuid"
)

// TaskType identifies the kind of LLM request within a file subtask.
type TaskType string

const (
	PlanTask              TaskType = "plan_task"
	MainTask              TaskType = "main_task"
	MemoryCompressionTask TaskType = "memory_compression_task"
	ReLocationTask        TaskType = "re_location_task"
	HypothesisReviewTask  TaskType = "hypothesis_review_task"
)

const (
	ReviewModeWorkspace = "workspace"
	ReviewModeRange     = "range"
	ReviewModeCommit    = "commit"
	ReviewModeFullScan  = "full_scan"
)

// SessionHistory is the top-level container for an entire CR run.
// It is safe for concurrent use by multiple goroutines.
type SessionHistory struct {
	mu          sync.Mutex
	SessionID   string
	RepoDir     string
	GitBranch   string
	Model       string
	ReviewMode  string
	DiffFrom    string
	DiffTo      string
	DiffCommit  string
	BizID       string
	StartTime   time.Time
	EndTime     time.Time
	persist     *jsonlWriter
	Scopes      map[string]*ScopeSession
	llmFailures int64
	// diff totals for the session_end record (cost normalization denominators);
	// set once diffs are loaded via SetDiffStats.
	diffFiles      int
	diffInsertions int64
	diffDeletions  int64
}

// SetDiffStats records the reviewed diff's size once it is known (after diff
// loading — later than session_start), for the session_end record. Metric
// slicing by change size depends on it.
func (sh *SessionHistory) SetDiffStats(files int, insertions, deletions int64) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.diffFiles = files
	sh.diffInsertions = insertions
	sh.diffDeletions = deletions
}

// ScopeSession holds the conversation records for one review scope: a Unit
// (plan / main / memory-compression / re-location all nest here), or a
// non-Unit pass (Hypothesis Review, or scan's per-file work). Keyed by scope ID
// so two Units in the same file don't collide and a cross-file Unit stays whole.
type ScopeSession struct {
	mu          sync.Mutex
	ID          string   // scope id: unit.ID, Lane ID, or a scan file path
	Kind        string   // "unit" | "file" | "lane"
	Scope       string   // func/file/callchain (units) | hypothesis_review | scan
	Paths       []string // member file(s)
	Path        string   // representative member path; comment anchor / log label
	TaskRecords map[TaskType][]*TaskRecord
	session     *SessionHistory // back-reference for JSONL persistence

	// Unit lifecycle (see docs/unit-model.md): open → closing (Close
	// called, async still in flight) → closed (debrief persisted). Scopes that
	// never Close (scan / Lane passes) just stay open — the lifecycle is
	// opt-in for scopes that produce a debrief.
	state         scopeState
	pendingAsync  int      // async tasks registered via BeginAsync, not yet ended
	parkedDebrief *Debrief // held while closing until the last async task ends
	lateWrites    int      // records appended after close — a detectable misuse
}

// scopeState is the unit lifecycle state.
type scopeState int

const (
	scopeOpen scopeState = iota
	scopeClosing
	scopeClosed
)

// Scope identifies a review sub-session for recording: a Unit, Lane, or a
// file-level pass. Callers build it from a unit.Unit, Review 2 Lane, or scan file path;
// SessionHistory keys ScopeSessions by ID.
type Scope struct {
	ID    string   // unit.ID, Lane ID, or scan file path
	Kind  string   // "unit" | "file" | "lane"
	Type  string   // func/file/callchain (units) | hypothesis_review | scan
	Paths []string // member file(s)
}

// Path returns the representative member path (comment anchor / log label).
func (s Scope) Path() string {
	if len(s.Paths) > 0 {
		return s.Paths[0]
	}
	return s.ID
}

// TaskRecord captures a single LLM request-response cycle within a file subtask.
type TaskRecord struct {
	ExecutionID     string
	Type            TaskType
	RequestNo       int           // sequential number within this task type
	RequestMessages []llm.Message // messages sent to LLM
	Response        *ResponseRecord
	ToolResults     []ToolResultRecord
	Duration        time.Duration
	Error           string
	scopeSession    *ScopeSession // back-reference for JSONL persistence
}

// ExecutionEnd is the terminal runtime fact for one Harness Execution. It is
// intentionally domain-free: Unit coverage and review judgments stay outside
// the Session protocol.
type ExecutionEnd struct {
	ID         string
	TaskType   TaskType
	Outcome    string
	Reason     string
	Turns      int
	ToolCalls  int
	ToolErrors int
	Duration   time.Duration
}

// ContextCompaction is one aggregate ContextManager rewrite. It deliberately
// omits individual compactor stages: observers care that the model-visible
// context changed, by how much, and whether a summary checkpoint was involved.
type ContextCompaction struct {
	Reason         string
	Committed      bool
	TokensBefore   int
	TokensAfter    int
	MessagesBefore int
	MessagesAfter  int
	Summarized     bool
}

// TokenUsage holds token usage for a single LLM request/response cycle.
// Uses actual token counts from the API response when available,
// falling back to local estimation via tiktoken.
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// ResponseRecord holds the parsed LLM response.
type ResponseRecord struct {
	Content    string
	Reasoning  string
	StopReason string
	ToolCalls  []llm.ToolCall
	Model      string
	Usage      *TokenUsage
}

// ToolResultRecord records the result of a tool call executed after the LLM response.
type ToolResultRecord struct {
	ToolCallID string
	ToolName   string
	Arguments  string
	Result     string
	OK         bool
	Duration   time.Duration
	Metadata   map[string]any
}

// SessionOptions holds optional metadata for a new session. The manifest
// fields (Features/ToolVersion/Params) make every transcript self-describe its
// configuration — eval joins on them instead of guessing which gates a run had.
type SessionOptions struct {
	ReviewMode string
	DiffFrom   string
	DiffTo     string
	DiffCommit string
	// BizID is an opaque execution identity owned by the caller. Session
	// persistence and viewers expose it without interpreting its format.
	BizID string

	// Features is the resolved feature-gate map (feature.Set.Resolved()).
	Features map[string]bool
	// ToolVersion identifies the ccr build ("v1.7.1 (dc030bd)").
	ToolVersion string
	// Params are the run's governing knobs (unit watermark, preload budget…) —
	// the confounders a metric trend must be conditioned on.
	Params map[string]any
	// GitHead is the repo's HEAD sha at review time — the anchor posterior
	// scans walk forward from.
	GitHead string
}

// Finding is a final post-Trial review comment persisted into the
// transcript, so the session file alone carries everything the posterior
// accuracy tier joins on: the fingerprint keys human labels, the symbol-id +
// lines key "did a later commit touch this" scans (with the manifest's
// git_head as the anchor). Investigative result-tool calls in llm_response
// records are pre-Trial and don't reflect what the review delivered.
type Finding struct {
	HypothesisID string
	Path         string
	StartLine    int
	EndLine      int
	SymbolID     string
	Fingerprint  string
	Alias        string
	Content      string
	Category     string // engine-declared class (bug/security/…) — see finding.Finding
	Severity     string // engine-declared importance (critical/high/medium/low)
}

// WriteFindings persists the run's delivered findings, one "finding" record each.
func (sh *SessionHistory) WriteFindings(findings []Finding) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil {
		return
	}
	for _, f := range findings {
		p.WriteFinding(f)
	}
}

// WriteArtifact persists a domain-owned structured artifact without teaching
// Harness session storage its schema. Runner uses this for intermediate review
// results so eval can inspect or replay stages independently.
func (sh *SessionHistory) WriteArtifact(kind string, data map[string]any) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil || kind == "" {
		return
	}
	p.WriteArtifact(kind, data)
}

// WriteContextProjected persists the identifiable context actually exposed to
// one model call. Projection 1 is the Initial Context denominator; subsequent
// projections reveal compaction and tool-result changes without interpretation.
func (sh *SessionHistory) WriteContextProjected(scope Scope, executionID string, taskType TaskType, projectionNo int, items []agentgo.ContextItem) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil || executionID == "" || projectionNo < 1 {
		return
	}
	p.WriteContextProjected(sh.GetOrCreateScope(scope), executionID, taskType, projectionNo, items)
}

// WriteExecutionEnd persists the authoritative terminal state for one
// Execution. An Execution belongs to a Scope, but a Lane may own many of them.
func (sh *SessionHistory) WriteExecutionEnd(scope Scope, end ExecutionEnd) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil || end.ID == "" {
		return
	}
	p.WriteExecutionEnd(sh.GetOrCreateScope(scope), end)
}

// WriteContextCompaction persists one context rewrite in execution order.
func (sh *SessionHistory) WriteContextCompaction(scope Scope, executionID string, taskType TaskType, compaction ContextCompaction) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil || executionID == "" {
		return
	}
	p.WriteContextCompaction(sh.GetOrCreateScope(scope), executionID, taskType, compaction)
}

// BoardPost is one bulletin published to the Review Team board during the run,
// persisted for attribution/replay (the board itself is in-memory).
type BoardPost struct {
	From    string
	Turn    int
	Level   int
	Paths   []string
	Symbols []string
	Text    string
}

// WriteBoardPosts persists the run's board bulletins as "board_post" records.
func (sh *SessionHistory) WriteBoardPosts(posts []BoardPost) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil {
		return
	}
	for _, b := range posts {
		p.WriteBoardPost(b.From, b.Turn, b.Level, b.Paths, b.Symbols, b.Text)
	}
}

// New creates a new SessionHistory with the given repo directory.
func New(repoDir, gitBranch, model string, opts SessionOptions) *SessionHistory {
	sessionID := uuid.V4()
	sh := &SessionHistory{
		SessionID:  sessionID,
		RepoDir:    repoDir,
		GitBranch:  gitBranch,
		Model:      model,
		ReviewMode: opts.ReviewMode,
		DiffFrom:   opts.DiffFrom,
		DiffTo:     opts.DiffTo,
		DiffCommit: opts.DiffCommit,
		BizID:      opts.BizID,
		StartTime:  time.Now(),
		Scopes:     make(map[string]*ScopeSession),
	}

	p, err := newJSONLWriter(sessionID, repoDir, gitBranch, model, opts)
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr session] warning: failed to create session writer: %v\n", err)
	} else {
		sh.persist = p
		p.WriteSessionStart(sh.StartTime)
	}

	return sh
}

// GetOrCreateScope returns the ScopeSession for the given scope, creating one
// if it doesn't exist yet. Keyed by scope ID, so every task of one Unit (or one
// run/file-level pass) lands in the same sub-session.
func (sh *SessionHistory) GetOrCreateScope(sc Scope) *ScopeSession {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	ss, ok := sh.Scopes[sc.ID]
	if !ok {
		ss = &ScopeSession{
			ID:          sc.ID,
			Kind:        sc.Kind,
			Scope:       sc.Type,
			Paths:       sc.Paths,
			Path:        sc.Path(),
			TaskRecords: make(map[TaskType][]*TaskRecord),
			session:     sh,
		}
		sh.Scopes[sc.ID] = ss
	} else {
		seen := make(map[string]bool, len(ss.Paths)+len(sc.Paths))
		for _, path := range ss.Paths {
			seen[path] = true
		}
		for _, path := range sc.Paths {
			if path != "" && !seen[path] {
				ss.Paths = append(ss.Paths, path)
				seen[path] = true
			}
		}
	}
	return ss
}

// Finalize marks the session as complete, sets the end time, and persists
// the final summary record.
func (sh *SessionHistory) Finalize() {
	sh.mu.Lock()
	sh.EndTime = time.Now()
	p := sh.persist
	duration := sh.EndTime.Sub(sh.StartTime)
	seen := make(map[string]bool)
	filesReviewed := make([]string, 0, len(sh.Scopes))
	for _, ss := range sh.Scopes {
		for _, p := range ss.Paths {
			if p != "" && !seen[p] {
				seen[p] = true
				filesReviewed = append(filesReviewed, p)
			}
		}
	}
	failures := atomic.LoadInt64(&sh.llmFailures)
	stats := diffStats{files: sh.diffFiles, insertions: sh.diffInsertions, deletions: sh.diffDeletions}
	sh.mu.Unlock()

	if p != nil {
		p.WriteSessionEnd(duration, filesReviewed, failures, stats)
	}
}

// AppendTaskRecord adds a new task record to this scope session for the given
// task type. It auto-assigns the RequestNo based on existing records and writes
// an llm_request record to the JSONL stream.
func (ss *ScopeSession) AppendTaskRecord(taskType TaskType, messages []llm.Message) *TaskRecord {
	return ss.appendTaskRecord("", taskType, messages)
}

// AppendExecutionTaskRecord records one model call inside a Harness
// Execution. Direct auxiliary calls may still use AppendTaskRecord, while all
// AgentGo loop calls carry the execution identity used by Viewer and eval.
func (ss *ScopeSession) AppendExecutionTaskRecord(executionID string, taskType TaskType, messages []llm.Message) *TaskRecord {
	return ss.appendTaskRecord(executionID, taskType, messages)
}

func (ss *ScopeSession) appendTaskRecord(executionID string, taskType TaskType, messages []llm.Message) *TaskRecord {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	// A write after close means some worker outlived the scope's declared end —
	// the record is kept (losing data helps nobody) but the misuse is counted
	// and voiced, because it also escaped the debrief's cost rollup.
	if ss.state == scopeClosed {
		ss.lateWrites++
		fmt.Fprintf(console.Err(), "[ccr session] warning: %s record appended to closed scope %q\n", taskType, ss.ID)
	}

	rec := &TaskRecord{
		ExecutionID:     executionID,
		Type:            taskType,
		RequestNo:       len(ss.TaskRecords[taskType]) + 1,
		RequestMessages: copyMessages(messages),
		scopeSession:    ss,
	}
	ss.TaskRecords[taskType] = append(ss.TaskRecords[taskType], rec)

	if p := ss.session.persist; p != nil {
		p.WriteLLMRequest(ss, executionID, taskType, rec.RequestNo, copyMessagesForJSON(messages))
	}

	return rec
}

// copyMessages returns a deep copy of a messages slice so that future mutations
// don't corrupt stored records.
func copyMessages(msgs []llm.Message) []llm.Message {
	cp := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		cp[i] = llm.Message{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
			ToolCalls:  append([]llm.ToolCall(nil), m.ToolCalls...),
		}
	}
	return cp
}

// copyMessagesForJSON produces a JSON-friendly slice for persistence.
func copyMessagesForJSON(msgs []llm.Message) any {
	type msg struct {
		Role       string `json:"role"`
		Content    any    `json:"content"`
		ToolCallID string `json:"tool_call_id,omitempty"`
	}
	out := make([]msg, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, msg{
			Role:       m.Role,
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		})
	}
	return out
}

// SetResponse records the LLM response in the most recent TaskRecord of the given type.
// It uses actual token usage from the API response when available, falling back to
// local estimation via tiktoken, and writes an llm_response record to the JSONL stream.
func (tr *TaskRecord) SetResponse(resp *llm.ChatResponse, duration time.Duration) {
	if resp == nil || len(resp.Choices) == 0 {
		tr.SetError(fmt.Errorf("empty response"), duration)
		return
	}
	choice := resp.Choices[0]
	content := ""
	if choice.Message.Content != nil {
		content = *choice.Message.Content
	}
	reasoning := choice.Message.ReasoningContent

	var promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens int
	if resp.Usage != nil {
		promptTokens = int(resp.Usage.PromptTokens)
		completionTokens = int(resp.Usage.CompletionTokens)
		cacheReadTokens = int(resp.Usage.CacheReadTokens)
		cacheWriteTokens = int(resp.Usage.CacheWriteTokens)
	} else {
		for _, m := range tr.RequestMessages {
			promptTokens += llm.CountTokens(m.ExtractText())
		}
		completionTokens = llm.CountTokens(content + reasoning)
	}

	usage := &TokenUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
	}

	tr.Response = &ResponseRecord{
		Content:    content,
		Reasoning:  reasoning,
		StopReason: choice.FinishReason,
		ToolCalls:  choice.Message.ToolCalls,
		Model:      resp.Model,
		Usage:      usage,
	}
	tr.Duration = duration

	if ss := tr.scopeSession; ss != nil {
		if p := ss.session.persist; p != nil {
			toolCallsJSON := make([]map[string]any, 0, len(choice.Message.ToolCalls))
			for _, tc := range choice.Message.ToolCalls {
				toolCallsJSON = append(toolCallsJSON, map[string]any{
					"id":        tc.ID,
					"name":      tc.Function.Name,
					"arguments": tc.Function.Arguments,
				})
			}
			p.WriteLLMResponse(ss, tr.ExecutionID, tr.Type, content, reasoning, choice.FinishReason, toolCallsJSON, resp.Model, *usage, duration)
		}
	}
}

// SetError records an error for this task record, writes an llm_error entry to
// the JSONL stream, and increments the session-level LLM failure counter.
func (tr *TaskRecord) SetError(err error, duration time.Duration) {
	tr.Error = err.Error()
	tr.Duration = duration

	if ss := tr.scopeSession; ss != nil {
		if p := ss.session.persist; p != nil {
			p.WriteLLMError(ss, tr.ExecutionID, tr.Type, tr.RequestNo, err.Error(), duration)
		}
		atomic.AddInt64(&ss.session.llmFailures, 1)
	}
}

// LateWrites reports how many records were appended after the scope closed —
// zero in a correct run; a positive count means some async worker escaped the
// lifecycle (and the debrief undercounts it).
func (ss *ScopeSession) LateWrites() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.lateWrites
}

// LLMFailures returns the total number of LLM request failures recorded during this session.
func (sh *SessionHistory) LLMFailures() int64 {
	return atomic.LoadInt64(&sh.llmFailures)
}

// AddToolResult appends a tool call result to this task record and writes a
// tool_call record to the JSONL stream.
func (tr *TaskRecord) AddToolResult(toolName, arguments, result string) {
	tr.AddToolResultWithMetadata("", toolName, arguments, result, true, 0, nil)
}

// AddToolResultWithMetadata records the stable call identity and execution
// outcome so concurrent calls to the same tool remain distinguishable.
func (tr *TaskRecord) AddToolResultWithMetadata(toolCallID, toolName, arguments, result string, ok bool, duration time.Duration, metadata map[string]any) {
	tr.ToolResults = append(tr.ToolResults, ToolResultRecord{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Arguments:  arguments,
		Result:     result,
		OK:         ok,
		Duration:   duration,
		Metadata:   metadata,
	})

	if ss := tr.scopeSession; ss != nil {
		if p := ss.session.persist; p != nil {
			p.WriteToolCall(ss, tr.ExecutionID, tr.Type, toolCallID, toolName, arguments, result, ok, duration, metadata)
		}
	}
}
