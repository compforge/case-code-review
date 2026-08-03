package viewer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeViewerSession(t *testing.T, root, repo, sessionID string, records ...string) {
	t.Helper()
	dir := filepath.Join(root, repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sessionStart(id string) string {
	return `{"type":"session_start","sessionId":"` + id + `","schema_version":8,"timestamp":"2026-08-02T00:00:00Z","model":"test-model"}`
}

func TestDiscoverReposOnlyIncludesCurrentSchema(t *testing.T) {
	root := t.TempDir()
	repo := "example-repo"
	writeViewerSession(t, root, repo, "old", `{"type":"session_start","schema_version":5,"biz_id":"pr:old"}`)
	writeViewerSession(t, root, repo, "new", `{"type":"session_start","schema_version":8,"biz_id":"pr:new"}`)
	now := time.Now()
	oldPath := filepath.Join(root, repo, "old.jsonl")
	newPath := filepath.Join(root, repo, "new.jsonl")
	if err := os.Chtimes(oldPath, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, now, now); err != nil {
		t.Fatal(err)
	}

	repos, err := DiscoverRepos(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].SessionCount != 1 || repos[0].LatestBizID != "pr:new" {
		t.Fatalf("repos = %+v", repos)
	}
}

func TestLoadSessionProjectsScopeExecutionsAndDecisionTrail(t *testing.T) {
	root, repo, sessionID := t.TempDir(), "example-repo", "session-1"
	writeViewerSession(t, root, repo, sessionID,
		`{"type":"session_start","sessionId":"session-1","schema_version":8,"model":"test-model","biz_id":"github:org/repo#148"}`,
		`{"type":"context_compacted","execution_id":"exec-unit","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","reason":"threshold","committed":true,"tokens_before":160,"tokens_after":100,"messages_before":3,"messages_after":2,"summarized":false}`,
		`{"type":"llm_request","execution_id":"exec-unit","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","request_no":1,"messages":[{"role":"system","content":"investigate"},{"role":"user","content":"review a.go"}]}`,
		`{"type":"llm_response","execution_id":"exec-unit","scope_id":"unit-1","kind":"unit","scope":"file","taskType":"main_task","content":"done","usage":{"prompt_tokens":100,"completion_tokens":10}}`,
		`{"type":"execution_end","execution_id":"exec-unit","scope_id":"unit-1","kind":"unit","scope":"file","taskType":"main_task","outcome":"completed","turns":1,"tool_calls":0,"tool_errors":0,"duration_ms":1200}`,
		`{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-1","origin_unit":"unit-1","path":"a.go"}}`,
		`{"type":"llm_request","execution_id":"exec-lane","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","paths":["a.go"],"filePath":"a.go","taskType":"hypothesis_review_task","request_no":1,"messages":[{"role":"system","content":"verify evidence"},{"role":"user","content":"assess h-1"}]}`,
		`{"type":"llm_response","execution_id":"exec-lane","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","content":"assessed","usage":{"prompt_tokens":80,"completion_tokens":8}}`,
		`{"type":"execution_end","execution_id":"exec-lane","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","outcome":"completed","turns":1,"tool_calls":1,"tool_errors":0,"duration_ms":900}`,
		`{"type":"artifact","artifact_kind":"review_lane_assignment","data":{"lane_id":"l-1","hypothesis_id":"h-1","paths":["a.go"]}}`,
		`{"type":"artifact","artifact_kind":"review_assessment","data":{"lane_id":"l-1","hypothesis_id":"h-1","submission_index":1}}`,
		`{"type":"artifact","artifact_kind":"trial_decision","data":{"lane_id":"l-1","hypothesis_id":"h-1","passed_trial":false}}`,
		`{"type":"session_end","files_reviewed":["a.go"],"duration_seconds":3}`,
	)

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Summary.SchemaVersion != 8 || got.Summary.BizID != "github:org/repo#148" {
		t.Fatalf("summary = %+v", got.Summary)
	}
	if len(got.Reviews) != 2 || len(got.Reviews[0].Executions) != 1 || len(got.Reviews[1].Executions) != 1 {
		t.Fatalf("reviews = %+v", got.Reviews)
	}
	if got.Reviews[0].Stage != Review1Stage || len(got.Reviews[0].Artifacts) != 1 {
		t.Fatalf("unit projection = %+v", got.Reviews[0])
	}
	if got.Reviews[1].Stage != Review2Stage || len(got.Reviews[1].Artifacts) != 3 {
		t.Fatalf("lane projection = %+v", got.Reviews[1])
	}
	execution := got.Reviews[0].Executions[0]
	if execution.Status != "completed" || len(execution.Conversation) != 3 || execution.Conversation[0].Kind != "compaction" || execution.Metrics.Compactions != 1 {
		t.Fatalf("execution = %+v", execution)
	}
	if got.Compaction.Count != 1 || got.Compaction.Summarized != 0 || len(execution.Conversation[1].Messages) != 2 {
		t.Fatalf("compaction/session projection = %+v %+v", got.Compaction, execution.Conversation)
	}
}

func TestLoadSessionSeparatesExecutionsWithinLane(t *testing.T) {
	root, repo, sessionID := t.TempDir(), "example-repo", "session-lane"
	writeViewerSession(t, root, repo, sessionID,
		sessionStart(sessionID),
		`{"type":"llm_request","execution_id":"exec-1","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","request_no":1,"messages":[{"role":"system","content":"verify"},{"role":"user","content":"h-1"}]}`,
		`{"type":"llm_response","execution_id":"exec-1","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","content":"done","usage":{"prompt_tokens":100,"completion_tokens":10}}`,
		`{"type":"execution_end","execution_id":"exec-1","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","outcome":"completed","duration_ms":1000}`,
		`{"type":"llm_request","execution_id":"exec-2","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","request_no":2,"messages":[{"role":"system","content":"verify"},{"role":"user","content":"h-2 with retained context"},{"role":"assistant","content":"prior"}]}`,
		`{"type":"llm_response","execution_id":"exec-2","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","content":"search","usage":{"prompt_tokens":180,"completion_tokens":10,"cache_read_tokens":120}}`,
		`{"type":"llm_request","execution_id":"exec-2","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","request_no":3,"messages":[{"role":"system","content":"verify"},{"role":"user","content":"condensed"}]}`,
		`{"type":"llm_response","execution_id":"exec-2","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","content":"done","usage":{"prompt_tokens":90,"completion_tokens":8,"cache_read_tokens":60}}`,
		`{"type":"execution_end","execution_id":"exec-2","scope_id":"hypothesis_review:l-1","kind":"lane","scope":"hypothesis_review","taskType":"hypothesis_review_task","outcome":"truncated","reason":"max_turns","duration_ms":2200}`,
		`{"type":"session_end","files_reviewed":["a.go"]}`,
	)

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	lane := got.Reviews[0]
	if len(lane.Executions) != 2 || lane.ExecutionDone != 1 || lane.ExecutionMissed != 1 {
		t.Fatalf("lane = %+v", lane)
	}
	second := lane.Executions[1]
	if len(second.Turns) != 2 || second.Turns[1].PromptDelta != -90 || second.Turns[1].MessageDelta != -1 {
		t.Fatalf("second execution turns = %+v", second.Turns)
	}
	if second.Status != "incomplete" || second.Reason != "max_turns" {
		t.Fatalf("second execution = %+v", second)
	}
}

func TestLoadSessionBuildsExecutionDiagnostics(t *testing.T) {
	root, repo, sessionID := t.TempDir(), "example-repo", "session-diagnostics"
	writeViewerSession(t, root, repo, sessionID,
		sessionStart(sessionID),
		`{"type":"llm_request","execution_id":"ok","scope_id":"unit-ok","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","messages":[{"role":"user","content":"review"}]}`,
		`{"type":"llm_response","execution_id":"ok","scope_id":"unit-ok","kind":"unit","scope":"file","taskType":"main_task","tool_calls":[{"id":"read-1","name":"read_files","arguments":"{\"file_path\":\"a.go\"}"}]}`,
		`{"type":"tool_call","execution_id":"ok","scope_id":"unit-ok","kind":"unit","scope":"file","taskType":"main_task","tool_call_id":"read-1","tool_name":"read_files","result":"Already available in the current context from the initial source context: a.go","ok":true}`,
		`{"type":"execution_end","execution_id":"ok","scope_id":"unit-ok","kind":"unit","scope":"file","taskType":"main_task","outcome":"completed","duration_ms":100}`,
		`{"type":"llm_request","execution_id":"cut","scope_id":"unit-cut","kind":"unit","scope":"file","paths":["b.go"],"filePath":"b.go","taskType":"main_task","messages":[{"role":"user","content":"review"}]}`,
		`{"type":"execution_end","execution_id":"cut","scope_id":"unit-cut","kind":"unit","scope":"file","taskType":"main_task","outcome":"truncated","reason":"max_turns","duration_ms":200}`,
		`{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-1","origin_unit":"unit-ok"}}`,
		`{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-2","origin_unit":"unit-cut"}}`,
		`{"type":"artifact","artifact_kind":"review_assessment","data":{"hypothesis_id":"h-1"}}`,
		`{"type":"artifact","artifact_kind":"trial_decision","data":{"hypothesis_id":"h-1","passed_trial":false}}`,
		`{"type":"session_end","files_reviewed":["a.go","b.go"],"diff_files":3}`,
	)

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	d := got.Diagnostics
	if d.Status != "warning" || d.Review1Units != 2 || d.Review1Executions != 2 || d.Review1Done != 1 || d.Review1Missed != 1 {
		t.Fatalf("diagnostics = %+v", d)
	}
	if d.Hypotheses != 2 || d.Assessments != 1 || d.Unassessed != 1 || d.TrialBlocked != 1 || d.CoveredReads != 1 {
		t.Fatalf("pipeline diagnostics = %+v", d)
	}
}

func TestLoadSessionBuildsBatchFileReadSignals(t *testing.T) {
	root, repo, sessionID := t.TempDir(), "example-repo", "session-tools"
	writeViewerSession(t, root, repo, sessionID,
		sessionStart(sessionID),
		`{"type":"llm_request","execution_id":"exec-1","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","messages":[{"role":"user","content":"review"}]}`,
		`{"type":"llm_response","execution_id":"exec-1","scope_id":"unit-1","kind":"unit","scope":"file","taskType":"main_task","reasoning":"independent paths","tool_calls":[{"id":"batch-1","name":"read_files","arguments":"{\"reads\":[{\"file_path\":\"a.go\"},{\"file_path\":\"caller.go\"},{\"file_path\":\"caller.go\",\"start_line\":20},{\"file_path\":\"other.go\"}]}"}]}`,
		`{"type":"tool_call","execution_id":"exec-1","scope_id":"unit-1","kind":"unit","scope":"file","taskType":"main_task","tool_call_id":"batch-1","tool_name":"read_files","result":"===== FILE_READ RESULT 1/4 =====\nAlready available in the current context from the initial source context: a.go lines 1-10.\n===== FILE_READ RESULT 2/4 =====\ncaller\n===== FILE_READ RESULT 3/4 =====\ncaller range\n===== FILE_READ RESULT 4/4 =====\nother","ok":true,"duration_ms":15}`,
		`{"type":"execution_end","execution_id":"exec-1","scope_id":"unit-1","kind":"unit","scope":"file","taskType":"main_task","outcome":"completed","duration_ms":100}`,
		`{"type":"debrief","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","source_preloads":["whole a.go"],"context_paths":{"caller":["caller.go"],"callee":["callee.go"]}}`,
		`{"type":"session_end","files_reviewed":["a.go"]}`,
	)

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	review := got.Reviews[0]
	tools := review.Executions[0].Turns[0].ToolCalls
	if len(tools) != 1 || !strings.Contains(tools[0].Result, "caller range") {
		t.Fatalf("tools = %+v", tools)
	}
	want := FileReadMetrics{Calls: 1, Requests: 4, UniqueFiles: 3, CoveredRequests: 1, SamePathRepeats: 1, PreloadedFiles: 1, UnitKnownFiles: 2, CallGraphFiles: 1}
	if review.FileReads != want {
		t.Fatalf("file read metrics = %+v, want %+v", review.FileReads, want)
	}
}

func TestLoadSessionRejectsOldSchema(t *testing.T) {
	root, repo, sessionID := t.TempDir(), "example-repo", "old"
	writeViewerSession(t, root, repo, sessionID, `{"type":"session_start","schema_version":5}`)
	if _, err := LoadSession(root, repo, sessionID); err == nil || !strings.Contains(err.Error(), "unsupported session schema") {
		t.Fatalf("error = %v", err)
	}
}

func TestPeekSessionLoadsCurrentSchemaSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	transcript := strings.Join([]string{
		`{"type":"session_start","schema_version":8,"timestamp":"2026-08-02T00:00:00Z","model":"test-model"}`,
		`{"type":"session_end","duration_seconds":2,"files_reviewed":["a.go","b.go"],"diff_files":5,"diff_insertions":30,"diff_deletions":6}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := peekSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 8 || !got.HasDiffStats || got.DiffFileCount != 5 || got.FileCount != 2 {
		t.Fatalf("summary = %+v", got)
	}
}
