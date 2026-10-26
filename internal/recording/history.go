// Package recording owns Review recording metadata, business artifacts and scope
// accounting. AgentGo owns local conversation Session files; timeline owns stages.
package recording

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/uuid"
	"github.com/qiankunli/case-code-review/internal/recording/operation"

	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
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

// Review is the top-level container for an entire CR run.
// It is safe for concurrent use by multiple goroutines.
type Review struct {
	mu             sync.Mutex
	SessionID      string
	RepoDir        string
	GitBranch      string
	Model          string
	ReviewMode     string
	DiffFrom       string
	DiffTo         string
	DiffCommit     string
	BizID          string
	StartTime      time.Time
	EndTime        time.Time
	persist        *jsonlWriter
	Scopes         map[string]*ReviewScope
	llmFailures    int64
	operationError string
	// diff totals for the session_end record (cost normalization denominators);
	// set once diffs are loaded via SetDiffStats.
	diffFiles      int
	diffInsertions int64
	diffDeletions  int64
}

// SetDiffStats records the reviewed diff's size once it is known (after diff
// loading — later than session_start), for the session_end record. Metric
// slicing by change size depends on it.
func (sh *Review) SetDiffStats(files int, insertions, deletions int64) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.diffFiles = files
	sh.diffInsertions = insertions
	sh.diffDeletions = deletions
}

// ReviewScope holds bounded counters and completion state for one Unit, Lane
// or scan scope. It does not retain model messages or tool results.
type ReviewScope struct {
	mu        sync.Mutex
	ID        string   // scope id: unit.ID, Lane ID, or a scan file path
	Kind      string   // "unit" | "file" | "lane"
	Scope     string   // func/file/callchain (units) | hypothesis_review | scan
	Paths     []string // member file(s)
	Path      string   // representative member path; comment anchor / log label
	rounds    map[string]int
	toolCalls map[string]int
	tokens    TokenUsage
	duration  time.Duration
	session   *Review // back-reference for JSONL persistence

	// Unit lifecycle (see docs/unit-model.md): open → closing (Close
	// called, async still in flight) → closed (debrief persisted). Scopes that
	// never Close (Lane / auxiliary passes) just stay open — the lifecycle is
	// opt-in for scopes that produce a debrief.
	outcome       string
	reason        string
	unassessed    []string
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
// Review keys ReviewScopes by ID.
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

// TaskRecord is transient per-call accounting, never an in-memory conversation history.
type TaskRecord struct {
	OperationID     operation.ID
	ExecutionID     string
	Type            TaskType
	RequestNo       int // sequential number within this task type
	RequestMessages []agentgo.Message
	// messages sent to LLM
	Duration     time.Duration
	Error        string
	scopeSession *ReviewScope // back-reference for JSONL persistence
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
	Estimated        bool `json:"estimated,omitempty"`
	PromptTokens     int  `json:"prompt_tokens"`
	CompletionTokens int  `json:"completion_tokens"`
	ReasoningTokens  int  `json:"reasoning_tokens,omitempty"`
	CacheReadTokens  int  `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int  `json:"cache_write_tokens,omitempty"`
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
	ExistingCode string
	Side         string
	OldPath      string
	HypothesisID string
	OriginUnit   string
	LaneID       string
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
func (sh *Review) WriteFindings(findings []Finding) {
	sh.WriteFindingsContext(context.Background(), findings)
}

func (sh *Review) WriteFindingsContext(ctx context.Context, findings []Finding) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil {
		return
	}
	for _, f := range findings {
		p.WriteFinding(f, sh.stageRef(ctx).OperationID)
	}
}

// Flush makes records written so far visible to live local observers. Normal
// runs need no manual flush; streaming Finding delivery uses it so timing joins
// never race the buffered transcript.
func (sh *Review) Flush() {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p != nil {
		p.Flush()
	}
}

// WriteArtifact persists a domain-owned structured artifact without teaching
// Harness session storage its schema. Runner uses this for intermediate review
// results so eval can inspect or replay stages independently.
func (sh *Review) WriteArtifact(kind string, data map[string]any) {
	sh.WriteArtifactContext(context.Background(), kind, data)
}

func (sh *Review) WriteArtifactContext(ctx context.Context, kind string, data map[string]any) {
	sh.mu.Lock()
	p := sh.persist
	sh.mu.Unlock()
	if p == nil || kind == "" {
		return
	}
	p.WriteArtifact(kind, data, sh.stageRef(ctx).OperationID)
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
func (sh *Review) WriteBoardPosts(posts []BoardPost) {
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

// New creates a new Review with the given repo directory.
func New(repoDir, gitBranch, model string, opts SessionOptions) *Review {
	sessionID := uuid.V4()
	sh := &Review{
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
		Scopes:     make(map[string]*ReviewScope),
	}

	p, err := newJSONLWriter(sessionID, repoDir, gitBranch, model, opts)
	if err != nil {
		fmt.Fprintf(console.Err(), "[ccr session] warning: failed to create session writer: %v\n", err)
	} else {
		sh.persist = p
		p.WriteSessionStart(sh.StartTime)
	}

	sh.startOperations()
	return sh
}

// GetOrCreateScope returns the ReviewScope for the given scope, creating one
// if it doesn't exist yet. Keyed by scope ID, so every task of one Unit (or one
// run/file-level pass) lands in the same sub-session.
func (sh *Review) GetOrCreateScope(sc Scope) *ReviewScope {
	sh.mu.Lock()
	defer sh.mu.Unlock()

	ss, ok := sh.Scopes[sc.ID]
	if !ok {
		ss = &ReviewScope{
			ID:     sc.ID,
			Kind:   sc.Kind,
			Scope:  sc.Type,
			Paths:  sc.Paths,
			Path:   sc.Path(),
			rounds: make(map[string]int), toolCalls: make(map[string]int),
			session: sh,
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
func (sh *Review) Finalize(operationErr ...error) {
	sh.mu.Lock()
	if !sh.EndTime.IsZero() {
		sh.mu.Unlock()
		return
	}
	sh.EndTime = time.Now()
	if err := errors.Join(operationErr...); err != nil {
		sh.operationError = err.Error()
	}
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

	completion := sh.Completion()
	finishErr := errors.Join(operationErr...)
	if finishErr == nil && !completion.Complete() {
		finishErr = fmt.Errorf("review %s: admitted work did not complete", completion.Status)
	}
	telemetry.ReportRecordingError(operation.Finish(sh.SessionID, finishErr))
	operation.Release(sh.SessionID)
	if p != nil {
		p.WriteSessionEnd(duration, filesReviewed, failures, stats, completion, sh.totalUsage())
	}
}

// AppendTaskRecord starts transient accounting for a call and assigns its
// request number. The scope retains counters, not requests or responses.
func (ss *ReviewScope) AppendTaskRecord(taskType TaskType, messages []agentgo.Message) *TaskRecord {
	return ss.appendTaskRecord("", taskType, messages)
}

// AppendExecutionTaskRecord records one model call inside a Harness
// Execution. Direct auxiliary calls may still use AppendTaskRecord, while all
// AgentGo loop calls carry the execution identity used by Viewer and eval.
func (ss *ReviewScope) AppendExecutionTaskRecord(executionID string, taskType TaskType, messages []agentgo.Message) *TaskRecord {
	return ss.appendTaskRecord(executionID, taskType, messages)
}

func (ss *ReviewScope) appendTaskRecord(executionID string, taskType TaskType, messages []agentgo.Message) *TaskRecord {
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
		OperationID:     operation.ID(uuid.V4()),
		Type:            taskType,
		RequestNo:       ss.rounds[string(taskType)] + 1,
		RequestMessages: copyMessages(messages),
		scopeSession:    ss,
	}
	ss.rounds[string(taskType)]++

	return rec
}

// copyMessages retains the message fields needed for local token estimation.
func copyMessages(messages []agentgo.Message) []agentgo.Message {
	return append([]agentgo.Message(nil), messages...)
}

// SetResponse updates scope counters using reported usage when available and
// local token estimation otherwise. It does not retain the response.
func (tr *TaskRecord) SetResponse(resp *agentgo.LLMResponse, duration time.Duration) {
	tr.setResponse(resp, duration, true)
}

// SetResult retains reported failure usage without counting partial tool calls.
func (tr *TaskRecord) SetResult(resp *agentgo.LLMResponse, err error, duration time.Duration) {
	if err == nil {
		tr.SetResponse(resp, duration)
		return
	}
	if resp != nil && resp.Message.Usage != nil {
		tr.setResponse(resp, 0, false)
	}
	tr.SetError(err, duration)
}

func (tr *TaskRecord) setResponse(resp *agentgo.LLMResponse, duration time.Duration, countTools bool) {
	if resp == nil {
		tr.SetError(fmt.Errorf("empty response"), duration)
		return
	}
	content := resp.Message.TextContent()
	reasoning := resp.Message.ThinkingContent()

	var promptTokens, completionTokens, reasoningTokens, cacheReadTokens, cacheWriteTokens int
	if resp.Message.Usage != nil {
		promptTokens = resp.Message.Usage.Input + resp.Message.Usage.CacheWrite
		completionTokens = resp.Message.Usage.Output
		reasoningTokens = resp.Message.Usage.Reasoning
		cacheReadTokens = resp.Message.Usage.CacheRead
		cacheWriteTokens = resp.Message.Usage.CacheWrite
	} else {
		for _, m := range tr.RequestMessages {
			promptTokens += llm.CountTokens(m.TextContent())
		}
		completionTokens = llm.CountTokens(content + reasoning)
	}

	usage := &TokenUsage{
		Estimated:        resp.Message.Usage == nil,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		ReasoningTokens:  reasoningTokens,
		CacheReadTokens:  cacheReadTokens,
		CacheWriteTokens: cacheWriteTokens,
	}

	tr.Duration = duration

	if ss := tr.scopeSession; ss != nil {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		ss.duration += duration
		ss.tokens.Estimated = ss.tokens.Estimated || usage.Estimated
		ss.tokens.PromptTokens += usage.PromptTokens
		ss.tokens.CompletionTokens += usage.CompletionTokens
		ss.tokens.ReasoningTokens += usage.ReasoningTokens
		ss.tokens.CacheReadTokens += usage.CacheReadTokens
		ss.tokens.CacheWriteTokens += usage.CacheWriteTokens
		if countTools {
			for _, call := range resp.Message.ToolCalls() {
				ss.toolCalls[call.Name]++
			}
		}
	}
}

// SetError accounts for duration and failures, except local budget rejection.
func (tr *TaskRecord) SetError(err error, duration time.Duration) {
	tr.Error = err.Error()
	tr.Duration = duration

	if ss := tr.scopeSession; ss != nil {
		ss.mu.Lock()
		ss.duration += duration
		ss.mu.Unlock()
		if !errors.Is(err, llm.ErrTokenBudget) {
			atomic.AddInt64(&ss.session.llmFailures, 1)
		}
	}
}

// LateWrites reports how many records were appended after the scope closed —
// zero in a correct run; a positive count means some async worker escaped the
// lifecycle (and the debrief undercounts it).
func (ss *ReviewScope) LateWrites() int {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.lateWrites
}

// LLMFailures returns the total number of LLM request failures recorded during this session.
func (sh *Review) LLMFailures() int64 {
	return atomic.LoadInt64(&sh.llmFailures)
}

// A record without a narrower producer belongs to the Session operation.
func (sh *Review) stageRef(ctx context.Context) operation.Ref {
	ref, _ := operation.FromContext(ctx)
	if ref.OperationID == "" {
		ref = operation.Ref{ReviewID: sh.SessionID, OperationID: operation.ID("operation:" + sh.SessionID)}
	}
	return ref
}

// totalUsage reads bounded accounting counters, never native Session bodies.
func (sh *Review) totalUsage() TokenUsage {
	sh.mu.Lock()
	scopes := make([]*ReviewScope, 0, len(sh.Scopes))
	for _, scope := range sh.Scopes {
		scopes = append(scopes, scope)
	}
	sh.mu.Unlock()
	var usage TokenUsage
	for _, scope := range scopes {
		scope.mu.Lock()
		usage.PromptTokens += scope.tokens.PromptTokens
		usage.CompletionTokens += scope.tokens.CompletionTokens
		usage.ReasoningTokens += scope.tokens.ReasoningTokens
		usage.CacheReadTokens += scope.tokens.CacheReadTokens
		usage.CacheWriteTokens += scope.tokens.CacheWriteTokens
		usage.Estimated = usage.Estimated || scope.tokens.Estimated
		scope.mu.Unlock()
	}
	return usage
}
