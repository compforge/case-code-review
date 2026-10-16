package recording

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/console"
)

// sessionSubDir is the subdirectory under ~/.casecodereview that holds session
// JSONL files. Tests flip it to "test-sessions" via UseTestSessions() so they
// don't pollute the real store.
var sessionSubDir = "sessions"

// SchemaVersion stamps every session_start so readers consume one explicit
// protocol instead of guessing old record semantics. v13 separates Review
// metadata, incremental timeline updates and native AgentGo Sessions.
const SchemaVersion = 13

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
	lease      *flock.Flock
	file       *os.File
	writer     *bufio.Writer
	sequence   uint64
	writeErr   error
	closed     bool
	startTime  time.Time
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

func (s *Review) artifactPath(ext string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".casecodereview", sessionSubDir, encodeRepoPath(s.RepoDir))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}
	return filepath.Join(dir, s.SessionID+ext), nil
}

// TranscriptPath returns the JSONL transcript produced by this session. It is
// exposed so local callers can join public Findings with pipeline timing facts
// without guessing CCR's storage layout.
func (s *Review) TranscriptPath() (string, error) {
	return s.artifactPath(".jsonl")
}

// LogPath returns this session's stderr-log path, co-located with its JSONL
// transcript (<sessions>/<encoded-repo>/<session-id>.log), creating the
// directory. Lets a run mirror its warnings/errors next to the model-call trace.
func (s *Review) LogPath() (string, error) {
	return s.artifactPath(".log")
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
	// Acquire the lease before publishing the transcript. Session IDs are never reused.
	lease := flock.New(filename+".lock", flock.SetPermissions(0600))
	locked, err := lease.TryLock()
	if err != nil {
		return fmt.Errorf("lock session: %w", err)
	}
	if !locked {
		return fmt.Errorf("session already active: %s", jw.sessionID)
	}
	f, err := os.OpenFile(filename, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		_ = lease.Close()
		return fmt.Errorf("open session file: %w", err)
	}

	jw.lease = lease
	jw.file = f
	jw.writer = bufio.NewWriter(f)
	return nil
}

func (jw *jsonlWriter) Flush() {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	if err := jw.writer.Flush(); err != nil {
		fmt.Fprintf(console.Err(), "[ccr session] failed to flush transcript: %v\n", err)
	}
}

func (jw *jsonlWriter) elapsedMilliseconds() int64 {
	if jw.startTime.IsZero() {
		return 0
	}
	return time.Since(jw.startTime).Milliseconds()
}

// WriteSessionStart writes the initial session_start record.
func (jw *jsonlWriter) WriteSessionStart(startTime time.Time) string {
	// WriteSessionStart runs before the Session is shared with workers; the
	// monotonic zero point remains immutable for the writer's lifetime.
	jw.startTime = startTime

	rec := map[string]any{
		"type":           "session_start",
		"schema_version": SchemaVersion,
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
	if jw.opts.BizID != "" {
		rec["biz_id"] = jw.opts.BizID
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
	return jw.writeRecordLocked(rec)
}

// addScopeFields stamps the scope identity onto a per-record map: scope_id/kind/
// scope/paths identify the review scope (a Unit, run-level Review, or scan pass); filePath
// is the representative member path, kept for comment anchoring and file rollups.
func addScopeFields(rec map[string]any, ss *ReviewScope) {
	rec["filePath"] = ss.Path
	rec["scope_id"] = ss.ID
	rec["kind"] = ss.Kind
	rec["scope"] = ss.Scope
	rec["paths"] = ss.Paths
}

func (jw *jsonlWriter) WriteDebrief(ss *ReviewScope, d Debrief) string {

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"type":        "debrief",
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
	if d.ContextPaths != nil {
		rec["context_paths"] = d.ContextPaths
	}
	if len(d.SourcePreloads) > 0 {
		rec["source_preloads"] = d.SourcePreloads
	}
	if len(d.InitialOutlineAttempts) > 0 {
		rec["initial_outline_attempts"] = d.InitialOutlineAttempts
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
	addStageFields(rec, jw.sessionID, timeline.StageID("operation:"+jw.sessionID))
	return jw.writeRecordLocked(rec)
}

// WriteBoardPost writes one "board_post" record — a bulletin published during
// the run, for attribution and replay (the board is otherwise in-memory).
func (jw *jsonlWriter) WriteBoardPost(from string, turn, level int, paths, symbols []string, text string) {

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"type":    "board_post",
		"from":    from,
		"turn":    turn,
		"level":   level,
		"paths":   paths,
		"symbols": symbols,
		"text":    text,
	}
	addStageFields(rec, jw.sessionID, timeline.StageID("operation:"+jw.sessionID))
	jw.writeRecordLocked(rec)
}

// WriteFinding writes one delivered finding as a "finding" record (see Finding).
func (jw *jsonlWriter) WriteFinding(f Finding, stageID timeline.StageID) string {

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"type":        "finding",
		"path":        f.Path,
		"start_line":  f.StartLine,
		"end_line":    f.EndLine,
		"fingerprint": f.Fingerprint,
		"content":     f.Content,
	}
	if f.ExistingCode != "" {
		rec["existing_code"] = f.ExistingCode
	}
	if f.Side != "" {
		rec["side"] = f.Side
	}
	if f.OldPath != "" {
		rec["old_path"] = f.OldPath
	}
	if f.SymbolID != "" {
		rec["symbol_id"] = f.SymbolID
	}
	if f.HypothesisID != "" {
		rec["hypothesis_id"] = f.HypothesisID
	}
	if f.OriginUnit != "" {
		rec["origin_unit"] = f.OriginUnit
	}
	if f.LaneID != "" {
		rec["lane_id"] = f.LaneID
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
	addStageFields(rec, jw.sessionID, stageID)
	return jw.writeRecordLocked(rec)
}

// WriteArtifact stores an opaque domain artifact under a stable envelope. The
// payload schema belongs to the caller; session persistence only supplies run
// identity, ordering, and timestamps.
func (jw *jsonlWriter) WriteArtifact(kind string, data map[string]any, stageID timeline.StageID) string {

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"type":          "artifact",
		"artifact_kind": kind,
		"stage_id":      stageID,
		"timeline_id":   jw.sessionID,
		"data":          data,
	}
	return jw.writeRecordLocked(rec)
}

// diffStats carries the reviewed diff's totals into the session_end record —
// the denominators cost metrics normalize by.
type diffStats struct {
	files                 int
	insertions, deletions int64
}

// WriteSessionEnd writes the final session_end summary record and closes the file.
func (jw *jsonlWriter) WriteSessionEnd(duration time.Duration, filesReviewed []string, llmFailures int64, stats diffStats) {

	jw.mu.Lock()
	defer jw.mu.Unlock()
	rec := map[string]any{
		"type":             "session_end",
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

	if jw.writer != nil {
		jw.writer.Flush()
	}
	jw.closed = true
	if jw.file != nil {
		jw.file.Close()
	}
	if jw.lease != nil {
		_ = jw.lease.Close()
	}
}

func (jw *jsonlWriter) flushAndClose() {
	jw.mu.Lock()
	defer jw.mu.Unlock()
	if jw.writer != nil {
		jw.writer.Flush()
	}
	jw.closed = true
	if jw.file != nil {
		jw.file.Close()
	}
	if jw.lease != nil {
		_ = jw.lease.Close()
	}
}
