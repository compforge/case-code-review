package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/go-stdx/timeline"
)

func TestExportSessionATIF(t *testing.T) {
	lines := `{"type":"session_start","sessionId":"s1","model":"m1","cwd":"/r","gitBranch":"b","reviewMode":"range","diffFrom":"origin/main","diffTo":"HEAD","tool_version":"v1.13.2 (abc123)","features":{"hypothesis_review":true},"params":{"unit_watermark":10},"git_head":"deadbeef","eval_tag":"replay:test","biz_id":"github:org/repo#148","timestamp":"2026-07-02T10:00:00Z","schema_version":11}
{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-1","path":"a.go"},"timestamp":"2026-07-02T10:00:00Z"}
{"type":"context_projected","scope_id":"u1","filePath":"a.go","kind":"unit","execution_id":"exec-1","projection_no":1,"items":[{"kind":"file","identity":"a.go","representation":"source","reason":"unit","ref":"a.go::F"}],"timestamp":"2026-07-02T10:00:00Z"}
{"type":"llm_request","scope_id":"u1","execution_id":"exec-1","filePath":"a.go","request_no":1,"messages":[{"role":"system","content":"be a reviewer"},{"role":"user","content":"diff here"}],"timestamp":"2026-07-02T10:00:01Z","stage_id":"request-1","timeline_id":"s1"}
{"type":"timeline_update","timeline_id":"s1","update":{"Stages":[{"revision":2,"id":"request-1","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:00:05+00:00","status":"succeeded"}]}}
{"type":"llm_response","scope_id":"u1","execution_id":"exec-1","filePath":"a.go","model":"m1","content":"","reasoning":"The changed file needs one more check.","tool_calls":[{"id":"c1","name":"read_files","arguments":"{\"reads\":[{\"file_path\":\"a.go\"}]}"}],"usage":{"prompt_tokens":100,"completion_tokens":10},"timestamp":"2026-07-02T10:00:06Z","stage_id":"request-1","timeline_id":"s1"}
{"type":"tool_result","scope_id":"u1","execution_id":"exec-1","tool_name":"read_files","arguments":"{\"reads\":[{\"file_path\":\"a.go\"}]}","result":"===== FILE_READ RESULT 1/1 =====\nFile: a.go (Total lines: 1)\nLINE_RANGE: 1-1\n1|package a","ok":true,"metadata":{"cache_status":"hit"},"timestamp":"2026-07-02T10:00:06Z","request_id":"request-1","tool_call_id":"c1","stage_id":"tool-1-c1","timeline_id":"s1"}
{"type":"llm_request","scope_id":"u1","execution_id":"exec-1","request_no":2,"messages":[{"role":"system","content":"be a reviewer"}],"timestamp":"2026-07-02T10:00:07Z","stage_id":"request-2","timeline_id":"s1"}
{"type":"timeline_update","timeline_id":"s1","update":{"Stages":[{"revision":2,"id":"request-2","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:00:03+00:00","status":"succeeded"}]}}
{"type":"llm_response","scope_id":"u1","execution_id":"exec-1","filePath":"a.go","model":"m1","content":"looks fine","usage":{"prompt_tokens":200,"completion_tokens":20},"timestamp":"2026-07-02T10:00:10Z","stage_id":"request-2","timeline_id":"s1"}
	{"type":"timeline_update","timeline_id":"s1","update":{"Stages":[{"revision":2,"id":"exec-1","parent_id":"operation:s1","name":"execution","started_at":"2026-08-02T00:00:00+00:00","status":"succeeded","fields":{"scope_id":"u1","filePath":"a.go","kind":"unit","execution_id":"exec-1","outcome":"completed","turns":2,"tool_calls":1,"task_type":"main_task"},"finished_at":"2026-08-02T00:00:08+00:00"}]}}
	{"type":"debrief","scope_id":"u1","filePath":"a.go","kind":"unit","initial_outline_attempts":[{"path":"b.go","language":"go","outcome":"admitted","bytes":42}],"timestamp":"2026-07-02T10:00:10Z"}
	`
	f := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(f, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	traj, err := exportSession(f)
	if err != nil {
		t.Fatal(err)
	}
	if traj.SchemaVersion != atifSchemaVersion || traj.SessionID != "s1" || traj.Agent.ModelName != "m1" {
		t.Fatalf("root header: %+v", traj)
	}
	if traj.Extra["branch"] != "b" || traj.Extra["diff_from"] != "origin/main" {
		t.Fatalf("root extra: %v", traj.Extra)
	}
	if traj.Agent.Version != "v1.13.2 (abc123)" || traj.Extra["git_head"] != "deadbeef" ||
		traj.Extra["eval_tag"] != "replay:test" || traj.Extra["biz_id"] != "github:org/repo#148" {
		t.Fatalf("engine identity missing: agent=%+v extra=%v", traj.Agent, traj.Extra)
	}
	artifacts, ok := traj.Extra["review_artifacts"].([]map[string]any)
	if !ok || len(artifacts) != 1 || artifacts[0]["kind"] != "review_hypothesis" {
		t.Fatalf("review artifacts missing: %v", traj.Extra["review_artifacts"])
	}
	if len(traj.Subagents) != 1 {
		t.Fatalf("want 1 subagent trajectory, got %d", len(traj.Subagents))
	}
	sub := traj.Subagents[0]
	if sub.TrajectoryID != "exec-1" || sub.Extra["file_path"] != "a.go" {
		t.Fatalf("sub header: %+v", sub)
	}
	initialItems, ok := sub.Extra["initial_context"].([]map[string]any)
	if !ok || len(initialItems) != 1 || initialItems[0]["reason"] != "unit" || initialItems[0]["kind"] != "file" {
		t.Fatalf("initial context missing: %v", sub.Extra["initial_context"])
	}
	projections, ok := sub.Extra["context_projections"].([]map[string]any)
	if !ok || len(projections) != 1 || projections[0]["projection_no"] != 1 {
		t.Fatalf("context projections missing: %v", sub.Extra["context_projections"])
	}
	if sub.Extra["execution_outcome"] != "completed" || sub.Extra["execution_id"] != "exec-1" || sub.Extra["execution_turns"] != 2 {
		t.Fatalf("execution terminal fact missing: %v", sub.Extra)
	}
	outlineAttempts, ok := sub.Extra["initial_outline_attempts"].([]map[string]any)
	if !ok || len(outlineAttempts) != 1 || outlineAttempts[0]["outcome"] != "admitted" {
		t.Fatalf("initial outline attempts missing: %v", sub.Extra["initial_outline_attempts"])
	}
	// Steps: system + user (from request #1 only — request #2's replayed
	// conversation must NOT duplicate them) + two agent responses.
	if len(sub.Steps) != 4 {
		t.Fatalf("want 4 steps, got %d", len(sub.Steps))
	}
	if sub.Steps[0].Source != "system" || sub.Steps[1].Source != "user" || sub.Steps[1].Message != "diff here" {
		t.Fatalf("seed steps wrong: %+v %+v", sub.Steps[0], sub.Steps[1])
	}
	st := sub.Steps[2]
	if st.Source != "agent" || len(st.ToolCalls) != 1 || st.ToolCalls[0].FunctionName != "read_files" {
		t.Fatalf("agent step: %+v", st)
	}
	if st.ReasoningContent != "The changed file needs one more check." {
		t.Fatalf("reasoning content missing: %+v", st)
	}
	reads, ok := st.ToolCalls[0].Arguments["reads"].([]any)
	if !ok || len(reads) != 1 || reads[0].(map[string]any)["file_path"] != "a.go" {
		t.Fatalf("arguments not decoded: %v", st.ToolCalls[0].Arguments)
	}
	if st.Observation == nil || len(st.Observation.Results) != 1 ||
		st.Observation.Results[0].SourceCallID != "c1" || !strings.Contains(st.Observation.Results[0].Content, "1|package a") {
		t.Fatalf("observation pairing: %+v", st.Observation)
	}
	if st.Observation.Results[0].Extra["cache_status"] != "hit" {
		t.Fatalf("tool metadata missing: %+v", st.Observation.Results[0].Extra)
	}
	if st.Metrics.PromptTokens != 100 || st.Metrics.CompletionTokens != 10 {
		t.Fatalf("metrics: %+v", st.Metrics)
	}
	if sub.FinalMetrics.TotalPromptTokens != 300 || sub.FinalMetrics.TotalCompletionTokens != 30 {
		t.Fatalf("sub final: %+v", sub.FinalMetrics)
	}
	if traj.FinalMetrics.TotalSteps != 4 {
		t.Fatalf("root final: %+v", traj.FinalMetrics)
	}
}

func TestParseRawToolCallShapes(t *testing.T) {
	// flat (what sessions record) and OpenAI-nested both decode.
	id, name, args := parseRawToolCall(map[string]any{"id": "c1", "name": "f", "arguments": `{"k":1}`})
	if id != "c1" || name != "f" || args["k"] != float64(1) {
		t.Fatalf("flat: %s %s %v", id, name, args)
	}
	_, name2, args2 := parseRawToolCall(map[string]any{
		"id": "c2", "function": map[string]any{"name": "g", "arguments": `{"x":"y"}`}})
	if name2 != "g" || args2["x"] != "y" {
		t.Fatalf("nested: %s %v", name2, args2)
	}
	_, _, args3 := parseRawToolCall(map[string]any{"id": "c3", "name": "h", "arguments": "not-json"})
	if args3["raw"] != "not-json" {
		t.Fatalf("unparseable args must survive raw: %v", args3)
	}
}

func TestExportSessionPreservesLLMFailure(t *testing.T) {
	lines := `{"type":"session_start","sessionId":"s1","model":"m1","timestamp":"2026-07-02T10:00:00Z","schema_version":11}
{"type":"timeline_update","timeline_id":"s1","update":{"Stages":[{"revision":2,"id":"request-2","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:03:00+00:00","status":"succeeded"}]}}
{"type":"llm_error","scope_id":"u1","error":"routing timed out","failure":{"kind":"llm","phase":"routing","error_type":"timeout","code":"routing_budget_exhausted","attributes":{"request_phase":"await_response","response_started":false}},"timestamp":"2026-07-02T10:03:00Z","stage_id":"request-2","timeline_id":"s1"}
`
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	trajectory, err := exportSession(path)
	if err != nil {
		t.Fatal(err)
	}
	step := trajectory.Subagents[0].Steps[0]
	failure, ok := step.Extra["failure"].(map[string]any)
	if !ok || failure["phase"] != "routing" || failure["code"] != "routing_budget_exhausted" {
		t.Fatalf("exported failure = %+v", step.Extra["failure"])
	}
	attributes := failure["attributes"].(map[string]any)
	if attributes["request_phase"] != "await_response" {
		t.Fatalf("exported failure attributes = %+v", attributes)
	}
}

func TestExportSessionKeepsUnfinishedRequestTimeline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	text := `{"type":"session_start","sessionId":"s1","schema_version":11}
{"type":"llm_request","scope_id":"u1","kind":"unit","execution_id":"e1","taskType":"main_task","request_no":2,"messages":[],"stage_id":"request-3","timeline_id":"s1"}
{"type":"timeline_update","scope_id":"u1","execution_id":"e1","taskType":"main_task","request_no":2,"timeline_id":"s1","update":{"Operation":{"revision":1,"operation":"review","started_at":"2026-10-01T00:00:00Z","status":"running"},"Stages":[{"revision":1,"id":"wait-1","parent_id":"operation:s1","name":"await_response","started_at":"2026-10-01T00:00:00Z","status":"running"}]}}
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	trajectory, err := exportSession(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := trajectory.Extra["timeline"].(timeline.Document)
	if doc.Status != timeline.Running || len(doc.Stages) != 1 || doc.Stages[0].Status != timeline.Running {
		t.Fatalf("document=%+v", doc)
	}
	if _, err := json.Marshal(trajectory); err != nil {
		t.Fatal(err)
	}
	if trajectory.FinalMetrics.TotalSteps != 0 {
		t.Fatalf("timeline invented model steps: %+v", trajectory.FinalMetrics)
	}
}
