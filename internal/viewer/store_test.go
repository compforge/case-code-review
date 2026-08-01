package viewer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSessionKeepsAgentcorePromptsAndReviewArtifacts(t *testing.T) {
	root := t.TempDir()
	repo := "example-repo"
	sessionID := "session-1"
	dir := filepath.Join(root, repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"session_start","sessionId":"session-1","model":"test-model","biz_id":"github:org/repo#148"}`,
		`{"type":"llm_request","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","request_no":1,"messages":[{"role":"system","content":"investigate"},{"role":"user","content":"review a.go"}]}`,
		`{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-1","path":"a.go"}}`,
		`{"type":"llm_request","scope_id":"hypothesis_review:case-1","kind":"run","scope":"hypothesis_review","paths":["a.go"],"filePath":"a.go","taskType":"hypothesis_review_task","request_no":1,"messages":[{"role":"system","content":"verify evidence"},{"role":"user","content":"assess h-1"}]}`,
		`{"type":"artifact","artifact_kind":"review_assessment","data":{"hypothesis_id":"h-1","passed_trial":false}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SystemPrompts) != 2 {
		t.Fatalf("system prompts = %d, want 2", len(got.SystemPrompts))
	}
	if got.Summary.BizID != "github:org/repo#148" {
		t.Fatalf("biz id = %q", got.Summary.BizID)
	}
	if len(got.Reviews) != 2 || got.Reviews[0].Stage != Review1Stage || got.Reviews[1].Stage != Review2Stage {
		t.Fatalf("reviews = %+v, want Review 1 plus Review 2", got.Reviews)
	}
	if len(got.Artifacts) != 2 || !strings.Contains(got.Artifacts[1].Data, `"passed_trial": false`) {
		t.Fatalf("artifacts = %+v", got.Artifacts)
	}
}

func TestLoadSessionBuildsReviewOverviewAndPromptGrowth(t *testing.T) {
	root := t.TempDir()
	repo := "example-repo"
	sessionID := "session-stats"
	dir := filepath.Join(root, repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"session_start","sessionId":"session-stats","timestamp":"2026-07-31T00:00:00Z","model":"test-model"}`,
		`{"type":"llm_request","timestamp":"2026-07-31T00:00:01Z","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","request_no":1,"messages":[{"role":"system","content":"investigate"},{"role":"user","content":"review a.go"}]}`,
		`{"type":"llm_response","timestamp":"2026-07-31T00:00:02Z","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","model":"test-model","content":"searching","tool_calls":[{"name":"code_search","arguments":"{}"}],"duration_ms":1000,"usage":{"prompt_tokens":100,"completion_tokens":10,"cache_read_tokens":40,"cache_write_tokens":0}}`,
		`{"type":"tool_call","timestamp":"2026-07-31T00:00:03Z","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","tool_name":"code_search","result":"match","ok":true,"duration_ms":20}`,
		`{"type":"llm_request","timestamp":"2026-07-31T00:00:04Z","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","request_no":2,"messages":[{"role":"system","content":"investigate"},{"role":"user","content":"review a.go"},{"role":"assistant","content":"searching"},{"role":"user","content":"match"}]}`,
		`{"type":"llm_response","timestamp":"2026-07-31T00:00:05Z","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","model":"test-model","content":"done","duration_ms":1200,"usage":{"prompt_tokens":150,"completion_tokens":15,"cache_read_tokens":90,"cache_write_tokens":0}}`,
		`{"type":"debrief","timestamp":"2026-07-31T00:00:06Z","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go"}`,
		`{"type":"llm_request","timestamp":"2026-07-31T00:00:07Z","scope_id":"hypothesis_review:case-1","kind":"run","scope":"hypothesis_review","paths":["a.go"],"filePath":"a.go","taskType":"hypothesis_review_task","request_no":1,"messages":[{"role":"system","content":"verify"},{"role":"user","content":"assess"}]}`,
		`{"type":"llm_response","timestamp":"2026-07-31T00:00:08Z","scope_id":"hypothesis_review:case-1","kind":"run","scope":"hypothesis_review","paths":["a.go"],"filePath":"a.go","taskType":"hypothesis_review_task","model":"test-model","content":"assessed","duration_ms":900,"usage":{"prompt_tokens":80,"completion_tokens":8,"cache_read_tokens":0,"cache_write_tokens":0}}`,
		`{"type":"debrief","timestamp":"2026-07-31T00:00:09Z","scope_id":"hypothesis_review:case-1","kind":"run","scope":"hypothesis_review","paths":["a.go"],"filePath":"a.go"}`,
		`{"type":"session_end","timestamp":"2026-07-31T00:00:10Z","duration_seconds":10,"files_reviewed":["a.go"]}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Reviews) != 2 {
		t.Fatalf("reviews = %d, want 2", len(got.Reviews))
	}
	review1 := got.Reviews[0]
	if review1.Stage != Review1Stage || review1.Metrics.TurnCount != 2 || review1.Metrics.PromptTokens != 250 {
		t.Fatalf("Review 1 metrics = %+v, stage = %s", review1.Metrics, review1.Stage)
	}
	if review1.Metrics.ElapsedSec != 5 || review1.Metrics.LLMDurationMs != 2200 {
		t.Fatalf("Review 1 timing = elapsed %.1f, llm %d", review1.Metrics.ElapsedSec, review1.Metrics.LLMDurationMs)
	}
	if review1.Turns[1].PromptDelta != 50 || review1.Turns[1].MessageDelta != 2 {
		t.Fatalf("second turn deltas = prompt %d, messages %d", review1.Turns[1].PromptDelta, review1.Turns[1].MessageDelta)
	}
	if len(review1.Tools) != 1 || review1.Tools[0].Name != "code_search" || review1.Tools[0].Calls != 1 {
		t.Fatalf("Review 1 tools = %+v", review1.Tools)
	}
	if got.TokenUsage.TotalPromptTokens != 330 || got.TokenUsage.RequestCount != 3 {
		t.Fatalf("session tokens = %+v", got.TokenUsage)
	}
	if len(got.ToolUsage) != 1 || got.ToolUsage[0].DurationMs != 20 {
		t.Fatalf("session tools = %+v", got.ToolUsage)
	}
}

func TestLoadSessionBuildsFileReadSignals(t *testing.T) {
	root := t.TempDir()
	repo := "example-repo"
	sessionID := "session-file-reads"
	dir := filepath.Join(root, repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"llm_request","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","request_no":1,"messages":[{"role":"user","content":"review"}]}`,
		`{"type":"llm_response","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","content":"reading","tool_calls":[{"name":"file_read","arguments":"{\"file_path\":\"a.go\"}"},{"name":"file_read","arguments":"{\"file_path\":\"caller.go\"}"},{"name":"file_read","arguments":"{\"file_path\":\"caller.go\",\"start_line\":20}"},{"name":"file_read","arguments":"{\"file_path\":\"other.go\"}"}]}`,
		`{"type":"tool_call","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","tool_name":"file_read","result":"Already available in the current context from the initial briefing: a.go lines 1-10.","ok":true}`,
		`{"type":"tool_call","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","tool_name":"file_read","result":"caller","ok":true}`,
		`{"type":"tool_call","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","tool_name":"file_read","result":"caller range","ok":true}`,
		`{"type":"tool_call","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","tool_name":"file_read","result":"other","ok":true}`,
		`{"type":"debrief","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","materials":["whole a.go"],"context_paths":{"caller":["caller.go"],"callee":["callee.go"]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Reviews) != 1 {
		t.Fatalf("reviews = %d, want 1", len(got.Reviews))
	}
	review := got.Reviews[0]
	want := FileReadMetrics{Calls: 4, UniqueFiles: 3, CoveredCalls: 1, SamePathRepeats: 1, MaterialFiles: 1, UnitKnownFiles: 2, CallGraphFiles: 1}
	if review.FileReads != want {
		t.Fatalf("file read metrics = %+v, want %+v", review.FileReads, want)
	}
	if !review.HasMaterials || !review.HasContextPaths {
		t.Fatalf("debrief coverage flags missing: %+v", review)
	}
}
