package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/qiankunli/case-code-review/internal/stdout"
	"github.com/qiankunli/go-stdx/uuid"
)

// sessionSubDir is the subdirectory under ~/.casecodereview that holds session
// JSONL files. Tests flip it to "test-sessions" via UseTestSessions() so they
// don't pollute the real store.
var sessionSubDir = "sessions"

// schemaVersion stamps every session_start so longitudinal analysis survives
// record-format changes. Bump when a record type's meaning changes (not for
// additive fields). v2: debrief records + run manifest.
const schemaVersion = 2

// evalTagEnv lets a run tag its transcript with the population it belongs to
// (fixed regression corpus vs rolling production) — the two aren't comparable,
// and without a tag collect-time separation is guesswork.
const evalTagEnv = "CCR_EVAL_TAG"

// jsonlWriter streams session records to a JSONL file under
// $HOME/.casecodereview/sessions/<encoded-repo-path>/<session-id>.jsonl.
// It is safe for concurrent use by multiple goroutines.
type jsonlWriter struct {
	mu         sync.Mutex
	sessionID  string
	repoDir    string
	gitBranch  string
	model      string
	reviewMode string
	diffFrom   string
	diffTo     string
	diffCommit string
	opts       SessionOptions // manifest fields (features/version/params)
	file       *os.File
	writer     *bufio.Writer
	lastUUID   string // tracks chain of records via parentUuid
}

// newJSONLWriter creates and opens a new JSONL writer for the given session.
func newJSONLWriter(sessionID, repoDir, gitBranch, model string, opts SessionOptions) (*jsonlWriter, error) {
	jw := &jsonlWriter{
		sessionID:  sessionID,
		repoDir:    repoDir,
		gitBranch:  gitBranch,
		model:      model,
		reviewMode: opts.ReviewMode,
		diffFrom:   opts.DiffFrom,
		diffTo:     opts.DiffTo,
		diffCommit: opts.DiffCommit,
		opts:       opts,
	}
	if err := jw.open(); err != nil {
		return nil, err
	}
	return jw, nil
}

func encodeRepoPath(p string) string {
	// Handle empty or invalid input
	if p == "" {
		return "empty"
	}

	vol := filepath.VolumeName(p)
	p = p[len(vol):]

	// Trim leading path separators
	p = strings.TrimLeft(p, "/\\")

	// Replace separators with -
	p = strings.ReplaceAll(p, "/", "-")
	p = strings.ReplaceAll(p, "\\", "-")

	// Replace colons (from Windows drive letters)
	vol = strings.ReplaceAll(vol, ":", "_")

	// Handle edge case where path was only separators or volume name
	result := vol + p
	if result == "" {
		return "empty"
	}
	return result
}

// LogPath returns this session's stderr-log path, co-located with its JSONL
// transcript (<sessions>/<encoded-repo>/<session-id>.log), creating the
// directory. Lets a run mirror its warnings/errors next to the model-call trace.
func (s *SessionHistory) LogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".casecodereview", sessionSubDir, encodeRepoPath(s.RepoDir))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	return filepath.Join(dir, s.SessionID+".log"), nil
}

func (jw *jsonlWriter) open() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}

	sessionDir := filepath.Join(home, ".casecodereview", sessionSubDir, encodeRepoPath(jw.repoDir))
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return fmt.Errorf("create session dir: %w", err)
	}

	filename := filepath.Join(sessionDir, jw.sessionID+".jsonl")
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open session file: %w", err)
	}

	jw.file = f
	jw.writer = bufio.NewWriter(f)
	return nil
}

func (jw *jsonlWriter) writeRecordLocked(rec map[string]any) {
	data, err := json.Marshal(rec)
	if err != nil {
		fmt.Fprintf(stdout.Err(), "[ccr session] failed to marshal record: %v\n", err)
		return
	}
	jw.writer.Write(data)
	jw.writer.WriteByte('\n')
}

// WriteSessionStart writes the initial session_start record.
func (jw *jsonlWriter) WriteSessionStart(startTime time.Time) string {
	uuid := uuid.V4()
	rec := map[string]any{
		"uuid":           uuid,
		"parentUuid":     nil,
		"type":           "session_start",
		"sessionId":      jw.sessionID,
		"timestamp":      startTime.UTC().Format(time.RFC3339),
		"schema_version": schemaVersion,
		"cwd":            jw.repoDir,
		"gitBranch":      jw.gitBranch,
		"model":          jw.model,
	}
	if jw.reviewMode != "" {
		rec["reviewMode"] = jw.reviewMode
	}
	if jw.diffFrom != "" {
		rec["diffFrom"] = jw.diffFrom
	}
	if jw.diffTo != "" {
		rec["diffTo"] = jw.diffTo
	}
	if jw.diffCommit != "" {
		rec["diffCommit"] = jw.diffCommit
	}
	// Run manifest: configuration the metrics must join on / be conditioned on.
	if jw.opts.ToolVersion != "" {
		rec["tool_version"] = jw.opts.ToolVersion
	}
	if len(jw.opts.Features) > 0 {
		rec["features"] = jw.opts.Features
	}
	if len(jw.opts.Params) > 0 {
		rec["params"] = jw.opts.Params
	}
	if jw.opts.GitHead != "" {
		rec["git_head"] = jw.opts.GitHead
	}
	if tag := os.Getenv(evalTagEnv); tag != "" {
		rec["eval_tag"] = tag
	}

	jw.mu.Lock()
	defer jw.mu.Unlock()
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid
	return uuid
}

// addScopeFields stamps the scope identity onto a per-record map: scope_id/kind/
// scope/paths identify the review scope (a Unit, or a file-level pass); filePath
// is the representative member path, kept for comment anchoring and file rollups.
func addScopeFields(rec map[string]any, ss *ScopeSession) {
	rec["filePath"] = ss.Path
	rec["scope_id"] = ss.ID
	rec["kind"] = ss.Kind
	rec["scope"] = ss.Scope
	rec["paths"] = ss.Paths
}

// WriteLLMRequest writes a request entry with the resolved messages.
func (jw *jsonlWriter) WriteLLMRequest(ss *ScopeSession, taskType TaskType, requestNo int, messages any) string {
	uuid := uuid.V4()

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":       uuid,
		"parentUuid": jw.lastUUID,
		"type":       "llm_request",
		"sessionId":  jw.sessionID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"taskType":   string(taskType),
		"request_no": requestNo,
		"messages":   messages,
	}
	addScopeFields(rec, ss)
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid
	return uuid
}

// WriteLLMResponse writes a response entry with model, content, tool calls, usage.
func (jw *jsonlWriter) WriteLLMResponse(ss *ScopeSession, taskType TaskType, content string, toolCalls []map[string]any, model string, usage TokenUsage, duration time.Duration) string {
	uuid := uuid.V4()

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":        uuid,
		"parentUuid":  jw.lastUUID,
		"type":        "llm_response",
		"sessionId":   jw.sessionID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"taskType":    string(taskType),
		"model":       model,
		"content":     content,
		"tool_calls":  toolCalls,
		"duration_ms": duration.Milliseconds(),
		"usage": map[string]int{
			"prompt_tokens":      usage.PromptTokens,
			"completion_tokens":  usage.CompletionTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
		},
	}
	addScopeFields(rec, ss)
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid
	return uuid
}

// WriteLLMError writes an llm_error entry recording a failed LLM request.
func (jw *jsonlWriter) WriteLLMError(ss *ScopeSession, taskType TaskType, requestNo int, errorMsg string, duration time.Duration) string {
	uuid := uuid.V4()

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":        uuid,
		"parentUuid":  jw.lastUUID,
		"type":        "llm_error",
		"sessionId":   jw.sessionID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"taskType":    string(taskType),
		"request_no":  requestNo,
		"error":       errorMsg,
		"duration_ms": duration.Milliseconds(),
	}
	addScopeFields(rec, ss)
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid
	return uuid
}

// WriteToolCall writes a tool call result entry.
func (jw *jsonlWriter) WriteToolCall(ss *ScopeSession, taskType TaskType, toolName, arguments, result string, ok bool, duration time.Duration) string {
	uuid := uuid.V4()

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":        uuid,
		"parentUuid":  jw.lastUUID,
		"type":        "tool_call",
		"sessionId":   jw.sessionID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"taskType":    string(taskType),
		"tool_name":   toolName,
		"arguments":   arguments,
		"result":      result,
		"ok":          ok,
		"duration_ms": duration.Milliseconds(),
	}
	addScopeFields(rec, ss)
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid
	return uuid
}

// WriteDebrief writes a unit's terminal "debrief" record (see Debrief).
// Empty optional groups are omitted so the record stays greppable and small.
func (jw *jsonlWriter) WriteDebrief(ss *ScopeSession, d Debrief) string {
	uuid := uuid.V4()

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":        uuid,
		"parentUuid":  jw.lastUUID,
		"type":        "debrief",
		"sessionId":   jw.sessionID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"outcome":     d.Outcome,
		"formed":      d.Formed,
		"fragments":   d.Fragments,
		"insertions":  d.Insertions,
		"deletions":   d.Deletions,
		"usage_sites": d.UsageSites,
		"rounds":      d.Rounds,
		"duration_ms": d.DurationMs,
		"tokens": map[string]int{
			"prompt_tokens":      d.Tokens.PromptTokens,
			"completion_tokens":  d.Tokens.CompletionTokens,
			"cache_read_tokens":  d.Tokens.CacheReadTokens,
			"cache_write_tokens": d.Tokens.CacheWriteTokens,
		},
	}
	if d.Reason != "" {
		rec["reason"] = d.Reason
	}
	if len(d.Degradations) > 0 {
		rec["degradations"] = d.Degradations
	}
	if len(d.Clues) > 0 {
		rec["clues"] = d.Clues
	}
	if len(d.ClueRefs) > 0 {
		rec["clue_refs"] = d.ClueRefs
	}
	if len(d.Materials) > 0 {
		rec["materials"] = d.Materials
	}
	if len(d.ToolCalls) > 0 {
		rec["tool_calls"] = d.ToolCalls
	}
	if d.BoardPulled != 0 || d.BoardPosted != 0 {
		rec["board"] = map[string]int{
			"pulled":          d.BoardPulled,
			"injected_tokens": d.BoardInjectedTokens,
			"posted":          d.BoardPosted,
		}
	}
	addScopeFields(rec, ss)
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid
	return uuid
}

// WriteBoardPost writes one "board_post" record — a bulletin published during
// the run, for attribution and replay (the board is otherwise in-memory).
func (jw *jsonlWriter) WriteBoardPost(from string, turn, level int, paths, symbols []string, text string) {
	u := uuid.V4()
	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":       u,
		"parentUuid": jw.lastUUID,
		"type":       "board_post",
		"sessionId":  jw.sessionID,
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"from":       from,
		"turn":       turn,
		"level":      level,
		"paths":      paths,
		"symbols":    symbols,
		"text":       text,
	}
	jw.writeRecordLocked(rec)
	jw.lastUUID = u
}

// WriteFinding writes one delivered finding as a "finding" record (see Finding).
func (jw *jsonlWriter) WriteFinding(f Finding) string {
	uuid := uuid.V4()

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":        uuid,
		"parentUuid":  jw.lastUUID,
		"type":        "finding",
		"sessionId":   jw.sessionID,
		"timestamp":   time.Now().UTC().Format(time.RFC3339),
		"path":        f.Path,
		"start_line":  f.StartLine,
		"end_line":    f.EndLine,
		"fingerprint": f.Fingerprint,
		"content":     f.Content,
	}
	if f.SymbolID != "" {
		rec["symbol_id"] = f.SymbolID
	}
	if f.Alias != "" {
		rec["alias"] = f.Alias
	}
	if f.Category != "" {
		rec["category"] = f.Category
	}
	if f.Severity != "" {
		rec["severity"] = f.Severity
	}
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid
	return uuid
}

// diffStats carries the reviewed diff's totals into the session_end record —
// the denominators cost metrics normalize by.
type diffStats struct {
	files                 int
	insertions, deletions int64
}

// WriteSessionEnd writes the final session_end summary record and closes the file.
func (jw *jsonlWriter) WriteSessionEnd(duration time.Duration, filesReviewed []string, llmFailures int64, stats diffStats) {
	uuid := uuid.V4()

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"uuid":             uuid,
		"parentUuid":       jw.lastUUID,
		"type":             "session_end",
		"sessionId":        jw.sessionID,
		"timestamp":        time.Now().UTC().Format(time.RFC3339),
		"files_reviewed":   filesReviewed,
		"duration_seconds": duration.Seconds(),
		"llm_failures":     llmFailures,
	}
	if stats.files > 0 {
		rec["diff_files"] = stats.files
		rec["diff_insertions"] = stats.insertions
		rec["diff_deletions"] = stats.deletions
	}
	jw.writeRecordLocked(rec)
	jw.lastUUID = uuid

	if jw.writer != nil {
		jw.writer.Flush()
	}
	if jw.file != nil {
		jw.file.Close()
	}
}

func (jw *jsonlWriter) flushAndClose() {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	if jw.writer != nil {
		jw.writer.Flush()
	}
	if jw.file != nil {
		jw.file.Close()
	}
}
