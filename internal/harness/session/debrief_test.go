package session

import (
	"os"
	"time"

	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestWriteDebrief(t *testing.T) {
	t.Setenv(evalTagEnv, "corpus-v1")
	repoDir := t.TempDir()
	sh := New(repoDir, "main", "test-model", SessionOptions{
		ReviewMode:  ReviewModeWorkspace,
		Features:    map[string]bool{"usage_sites": true},
		ToolVersion: "v1.7.1 (abc123)",
		Params:      map[string]any{"unit_watermark": 10},
	})
	sh.SetDiffStats(2, 30, 5)

	// Simulate one unit's traffic: 2 main rounds (one with a tool call) + 1 plan.
	sc := Scope{ID: "a.go#F", Kind: "unit", Type: "func", Paths: []string{"a.go"}}
	ss := sh.GetOrCreateScope(sc)
	mk := func(taskType TaskType, toolName string) {
		rec := &TaskRecord{Type: taskType, scopeSession: ss}
		rec.Duration = 2 * time.Second
		rr := &ResponseRecord{Usage: &TokenUsage{PromptTokens: 100, CompletionTokens: 10, CacheReadTokens: 40}}
		if toolName != "" {
			rr.ToolCalls = []llm.ToolCall{{Function: llm.FunctionCall{Name: toolName}}}
		}
		rec.Response = rr
		ss.TaskRecords[taskType] = append(ss.TaskRecords[taskType], rec)
	}
	mk(MainTask, "file_read")
	mk(MainTask, "task_done")
	mk(PlanTask, "")

	sh.CloseScope(sc, Debrief{
		Outcome:      "completed",
		Formed:       "func",
		Fragments:    1,
		Insertions:   12,
		Deletions:    3,
		Degradations: []string{"related_source_dropped"},
		Clues:        map[string]int{"caller/spec": 1},
		ClueRefs:     []string{"b.go::Entry"},
		Materials:    []string{"whole a.go"},
		UsageSites:   4,
	})
	sh.Finalize()

	records := readJSONLRecords(t, sessionJSONLPath(t, repoDir, sh.SessionID))

	var start, deb, end map[string]any
	for _, r := range records {
		switch r["type"] {
		case "session_start":
			start = r
		case "debrief":
			deb = r
		case "session_end":
			end = r
		}
	}

	// Manifest: the transcript self-describes its configuration and population.
	if start["schema_version"].(float64) != 2 || start["tool_version"] != "v1.7.1 (abc123)" ||
		start["eval_tag"] != "corpus-v1" {
		t.Fatalf("manifest fields off: %v", start)
	}
	if feats, _ := start["features"].(map[string]any); feats["usage_sites"] != true {
		t.Fatalf("features missing: %v", start["features"])
	}

	if deb == nil {
		t.Fatal("no debrief record written")
	}
	if deb["outcome"] != "completed" || deb["formed"] != "func" || deb["scope_id"] != "a.go#F" {
		t.Fatalf("debrief identity off: %v", deb)
	}
	// Cost rollup aggregated from the scope's task records, not caller-supplied.
	rounds := deb["rounds"].(map[string]any)
	if rounds["main_task"].(float64) != 2 || rounds["plan_task"].(float64) != 1 {
		t.Fatalf("rounds off: %v", rounds)
	}
	tools := deb["tool_calls"].(map[string]any)
	if tools["file_read"].(float64) != 1 || tools["task_done"].(float64) != 1 {
		t.Fatalf("tool_calls off: %v", tools)
	}
	tokens := deb["tokens"].(map[string]any)
	if tokens["prompt_tokens"].(float64) != 300 || tokens["cache_read_tokens"].(float64) != 120 {
		t.Fatalf("tokens off: %v", tokens)
	}
	if deb["duration_ms"].(float64) != 6000 {
		t.Fatalf("duration off: %v", deb["duration_ms"])
	}
	if deb["usage_sites"].(float64) != 4 || deb["insertions"].(float64) != 12 {
		t.Fatalf("briefing/size fields off: %v", deb)
	}

	// Diff totals land in session_end (cost normalization denominators).
	if end["diff_files"].(float64) != 2 || end["diff_insertions"].(float64) != 30 || end["diff_deletions"].(float64) != 5 {
		t.Fatalf("diff stats off: %v", end)
	}
}

func TestWriteFindings(t *testing.T) {
	repoDir := t.TempDir()
	sh := New(repoDir, "main", "test-model", SessionOptions{GitHead: "abc123def456"})
	sh.WriteFindings([]Finding{
		{Path: "a.go", StartLine: 3, EndLine: 5, SymbolID: "a.go::F", Fingerprint: "0123456789ab", Alias: "m1", Content: "off-by-one"},
		{Path: "b.go", StartLine: 9, EndLine: 9, Fingerprint: "ba9876543210", Content: "nil deref"},
	})
	sh.Finalize()

	records := readJSONLRecords(t, sessionJSONLPath(t, repoDir, sh.SessionID))
	var start map[string]any
	var findings []map[string]any
	for _, r := range records {
		switch r["type"] {
		case "session_start":
			start = r
		case "finding":
			findings = append(findings, r)
		}
	}
	// The posterior tier walks forward from the manifest's git_head anchor.
	if start["git_head"] != "abc123def456" {
		t.Fatalf("git_head missing from manifest: %v", start)
	}
	if len(findings) != 2 {
		t.Fatalf("want 2 finding records, got %d", len(findings))
	}
	f := findings[0]
	if f["path"] != "a.go" || f["symbol_id"] != "a.go::F" || f["fingerprint"] != "0123456789ab" ||
		f["start_line"].(float64) != 3 || f["content"] != "off-by-one" || f["alias"] != "m1" {
		t.Fatalf("finding record off: %v", f)
	}
	// Optional fields are omitted, not empty-stringed.
	if _, ok := findings[1]["symbol_id"]; ok {
		t.Fatalf("empty symbol_id must be omitted: %v", findings[1])
	}
}

func TestScopeLifecycle(t *testing.T) {
	repoDir := t.TempDir()
	sh := New(repoDir, "main", "test-model", SessionOptions{})
	sc := Scope{ID: "a.go#F", Kind: "unit", Type: "func", Paths: []string{"a.go"}}
	ss := sh.GetOrCreateScope(sc)

	// Async work in flight: Close parks the debrief instead of persisting.
	ss.BeginAsync()
	ss.Close(Debrief{Outcome: "completed", Formed: "func"})
	sh2 := readTypes(t, repoDir, sh.SessionID)
	if sh2["debrief"] != 0 {
		t.Fatal("debrief must be parked while async work is in flight")
	}

	// The last async task out finalizes: relocation-style record added by the
	// worker is INSIDE the rollup.
	rec := &TaskRecord{Type: ReLocationTask, scopeSession: ss, Duration: time.Second,
		Response: &ResponseRecord{Usage: &TokenUsage{PromptTokens: 7}}}
	ss.TaskRecords[ReLocationTask] = append(ss.TaskRecords[ReLocationTask], rec)
	ss.EndAsync()

	sh.Finalize()
	var deb map[string]any
	for _, r := range readJSONLRecords(t, sessionJSONLPath(t, repoDir, sh.SessionID)) {
		if r["type"] == "debrief" {
			deb = r
		}
	}
	if deb == nil {
		t.Fatal("debrief must persist once the last async task ends")
	}
	if deb["outcome"] != "completed" || deb["rounds"].(map[string]any)["re_location_task"].(float64) != 1 {
		t.Fatalf("parked debrief must include the async round: %v", deb)
	}

	// Closed scope: late writes are kept but counted; double Close keeps first.
	ss.AppendTaskRecord(MainTask, nil)
	if ss.LateWrites() != 1 {
		t.Fatalf("late write not detected: %d", ss.LateWrites())
	}
	ss.Close(Debrief{Outcome: "other"})
	if ss.state != scopeClosed {
		t.Fatal("second Close must not reopen")
	}
}

// readTypes counts record types in the session file so far (flushing first).
func readTypes(t *testing.T, repoDir, sessionID string) map[string]int {
	t.Helper()
	sh := map[string]int{}
	p := sessionJSONLPath(t, repoDir, sessionID)
	if _, err := os.Stat(p); err != nil {
		return sh
	}
	for _, r := range readJSONLRecords(t, p) {
		if s, ok := r["type"].(string); ok {
			sh[s]++
		}
	}
	return sh
}
