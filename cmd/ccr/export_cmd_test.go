package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportSessionATIF(t *testing.T) {
	lines := `{"type":"session_start","sessionId":"s1","model":"m1","cwd":"/r","gitBranch":"b","reviewMode":"range","diffFrom":"origin/main","diffTo":"HEAD","tool_version":"v1.13.2 (abc123)","features":{"hypothesis_review":true},"params":{"unit_watermark":10},"git_head":"deadbeef","eval_tag":"replay:test","biz_id":"github:org/repo#148","timestamp":"2026-07-02T10:00:00Z"}
{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-1","path":"a.go"},"timestamp":"2026-07-02T10:00:00Z"}
{"type":"llm_request","scope_id":"u1","filePath":"a.go","request_no":1,"messages":[{"role":"system","content":"be a reviewer"},{"role":"user","content":"diff here"}],"timestamp":"2026-07-02T10:00:01Z"}
{"type":"llm_response","scope_id":"u1","filePath":"a.go","model":"m1","content":"","tool_calls":[{"id":"c1","name":"read_files","arguments":"{\"reads\":[{\"file_path\":\"a.go\"}]}"}],"usage":{"prompt_tokens":100,"completion_tokens":10},"duration_ms":5000,"timestamp":"2026-07-02T10:00:06Z"}
{"type":"tool_call","scope_id":"u1","tool_name":"read_files","arguments":"{\"reads\":[{\"file_path\":\"a.go\"}]}","result":"===== FILE_READ RESULT 1/1 =====\nFile: a.go (Total lines: 1)\nLINE_RANGE: 1-1\n1|package a","ok":true,"timestamp":"2026-07-02T10:00:06Z"}
{"type":"llm_request","scope_id":"u1","request_no":2,"messages":[{"role":"system","content":"be a reviewer"}],"timestamp":"2026-07-02T10:00:07Z"}
{"type":"llm_response","scope_id":"u1","filePath":"a.go","model":"m1","content":"looks fine","usage":{"prompt_tokens":200,"completion_tokens":20},"duration_ms":3000,"timestamp":"2026-07-02T10:00:10Z"}
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
	if sub.TrajectoryID != "u1" || sub.Extra["file_path"] != "a.go" {
		t.Fatalf("sub header: %+v", sub)
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
	reads, ok := st.ToolCalls[0].Arguments["reads"].([]any)
	if !ok || len(reads) != 1 || reads[0].(map[string]any)["file_path"] != "a.go" {
		t.Fatalf("arguments not decoded: %v", st.ToolCalls[0].Arguments)
	}
	if st.Observation == nil || len(st.Observation.Results) != 1 ||
		st.Observation.Results[0].SourceCallID != "c1" || !strings.Contains(st.Observation.Results[0].Content, "1|package a") {
		t.Fatalf("observation pairing: %+v", st.Observation)
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
