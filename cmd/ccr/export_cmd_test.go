package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
)

func TestExportSessionATIF(t *testing.T) {
	lines := `{"type":"session_start","sessionId":"s1","model":"m1","cwd":"/r","gitBranch":"b","reviewMode":"range","diffFrom":"origin/main","diffTo":"HEAD","tool_version":"v1.13.2 (abc123)","features":{"hypothesis_review":true},"params":{"unit_watermark":10},"git_head":"deadbeef","eval_tag":"replay:test","biz_id":"github:org/repo#148","timestamp":"2026-07-02T10:00:00Z","schema_version":14}
{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-1","path":"a.go"},"timestamp":"2026-07-02T10:00:00Z"}
{"type":"context_projected","scope_id":"u1","filePath":"a.go","kind":"unit","execution_id":"exec-1","projection_no":1,"items":[{"kind":"file","identity":"a.go","representation":"source","reason":"unit","ref":"a.go::F"}],"timestamp":"2026-07-02T10:00:00Z"}
{"type":"llm_request","scope_id":"u1","execution_id":"exec-1","filePath":"a.go","request_no":1,"messages":[{"role":"system","content":"be a reviewer"},{"role":"user","content":"diff here"}],"timestamp":"2026-07-02T10:00:01Z","operation_id":"request-1","review_id":"s1"}
{"type":"operation","review_id":"s1","operation":{"id":"request-1","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:00:05+00:00","status":"succeeded"}}
{"type":"llm_response","scope_id":"u1","execution_id":"exec-1","filePath":"a.go","model":"m1","content":"","reasoning":"The changed file needs one more check.","tool_calls":[{"id":"c1","name":"read_files","arguments":"{\"reads\":[{\"file_path\":\"a.go\"}]}"}],"usage":{"prompt_tokens":100,"completion_tokens":10},"timestamp":"2026-07-02T10:00:06Z","operation_id":"request-1","review_id":"s1"}
{"type":"tool_result","scope_id":"u1","execution_id":"exec-1","tool_name":"read_files","arguments":"{\"reads\":[{\"file_path\":\"a.go\"}]}","result":"===== FILE_READ RESULT 1/1 =====\nFile: a.go (Total lines: 1)\nLINE_RANGE: 1-1\n1|package a","ok":true,"metadata":{"cache_status":"hit"},"timestamp":"2026-07-02T10:00:06Z","request_id":"request-1","tool_call_id":"c1","operation_id":"tool-1-c1","review_id":"s1"}
{"type":"llm_request","scope_id":"u1","execution_id":"exec-1","request_no":2,"messages":[{"role":"system","content":"be a reviewer"}],"timestamp":"2026-07-02T10:00:07Z","operation_id":"request-2","review_id":"s1"}
{"type":"operation","review_id":"s1","operation":{"id":"request-1","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:00:05+00:00","status":"succeeded"}}
{"type":"operation","review_id":"s1","operation":{"id":"request-2","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:00:03+00:00","status":"succeeded"}}
{"type":"llm_response","scope_id":"u1","execution_id":"exec-1","filePath":"a.go","model":"m1","content":"looks fine","usage":{"prompt_tokens":200,"completion_tokens":20},"timestamp":"2026-07-02T10:00:10Z","operation_id":"request-2","review_id":"s1"}
{"type":"operation","review_id":"s1","operation":{"id":"request-1","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:00:05+00:00","status":"succeeded"}}
{"type":"operation","review_id":"s1","operation":{"id":"request-2","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:00:03+00:00","status":"succeeded"}}
{"type":"operation","review_id":"s1","operation":{"id":"exec-1","parent_id":"operation:s1","name":"execution","started_at":"2026-08-02T00:00:00+00:00","status":"succeeded","finished_at":"2026-08-02T00:00:08+00:00","attributes":{"scope_id":"u1","filePath":"a.go","kind":"unit","execution_id":"exec-1","outcome":"completed","turns":2,"tool_calls":1,"task_type":"main_task"}}}
	{"type":"debrief","scope_id":"u1","filePath":"a.go","kind":"unit","initial_outline_attempts":[{"path":"b.go","language":"go","outcome":"admitted","bytes":42}],"timestamp":"2026-07-02T10:00:10Z"}
	`
	f := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(f, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	trajectories, err := exportSession(f)
	if err != nil {
		t.Fatal(err)
	}
	traj := trajectories[0]
	if traj.SchemaVersion != atifSchemaVersion || traj.SessionID != "" || traj.Extra["review_id"] != "s1" || traj.Agent.ModelName != "m1" {
		t.Fatalf("root header: %+v", traj)
	}
	if traj.Extra["branch"] != "b" || traj.Extra["diff_from"] != "origin/main" {
		t.Fatalf("root extra: %v", traj.Extra)
	}
	if traj.Agent.Version != "v1.13.2 (abc123)" || traj.Extra["git_head"] != "deadbeef" ||
		traj.Extra["eval_tag"] != "replay:test" || traj.Extra["biz_id"] != "github:org/repo#148" {
		t.Fatalf("engine identity missing: agent=%+v extra=%v", traj.Agent, traj.Extra)
	}
	if len(trajectories) != 1 {
		t.Fatalf("want one trajectory, got %d", len(trajectories))
	}
	sub := traj
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
	executions := sub.Extra["executions"].([]map[string]any)
	if len(executions) != 1 || executions[0]["outcome"] != "completed" || executions[0]["execution_id"] != "exec-1" || executions[0]["turns"] != 2 {
		t.Fatalf("executions=%v", executions)
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
		st.Observation.Results[0].SourceCallID != "c1" || !strings.Contains(st.Observation.Results[0].Content.(string), "1|package a") {
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
	lines := `{"type":"session_start","sessionId":"s1","model":"m1","timestamp":"2026-07-02T10:00:00Z","schema_version":14}
{"type":"operation","review_id":"s1","operation":{"id":"request-2","parent_id":"operation:s1","name":"llm.request","started_at":"2026-08-02T00:00:00+00:00","finished_at":"2026-08-02T00:03:00+00:00","status":"succeeded"}}
{"type":"llm_error","scope_id":"u1","error":"routing timed out","failure":{"kind":"llm","phase":"routing","error_type":"timeout","code":"routing_budget_exhausted","attributes":{"request_phase":"await_response","response_started":false}},"timestamp":"2026-07-02T10:03:00Z","operation_id":"request-2","review_id":"s1"}
`
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	trajectory, err := exportSession(path)
	if err != nil {
		t.Fatal(err)
	}
	step := trajectory[0].Steps[0]
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
	text := `{"type":"session_start","sessionId":"s1","schema_version":14}
{"type":"llm_request","scope_id":"u1","kind":"unit","execution_id":"e1","taskType":"main_task","request_no":2,"messages":[],"operation_id":"request-3","review_id":"s1"}
{"type":"operation","review_id":"s1","operation":{"id":"wait-1","parent_id":"operation:s1","name":"await_response","started_at":"2026-10-01T00:00:00Z","status":"running"}}
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	trajectory, err := exportSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trajectory) != 1 || len(trajectory[0].Steps) != 0 {
		t.Fatalf("invented model steps: %+v", trajectory)
	}
	pending := trajectory[0].Extra["pending_requests"].([]map[string]any)
	if len(pending) != 1 || pending[0]["request_id"] != "request-3" {
		t.Fatalf("pending=%v", pending)
	}
	if _, err := json.Marshal(trajectory); err != nil {
		t.Fatal(err)
	}
	if trajectory[0].FinalMetrics.TotalSteps != 0 {
		t.Fatalf("timeline invented model steps: %+v", trajectory[0].FinalMetrics)
	}
}

// +case=An unassigned tool result remains an observation without an invented model call
func TestExportUnassignedToolOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.jsonl")
	text := `{"type":"session_start","sessionId":"review","schema_version":14}
{"type":"tool_result","scope_id":"unit","execution_id":"exec","session_ref":{"session_id":"native"},"request_id":"missing","tool_call_id":"orphan","tool_name":"read_files","result":"evidence","ok":false}
`
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	trajectories, err := exportSession(path)
	if err != nil {
		t.Fatal(err)
	}
	step := trajectories[0].Steps[0]
	if *step.LLMCallCount != 0 || step.Metrics != nil || len(step.ToolCalls) != 0 || step.Observation.Results[0].SourceCallID != "orphan" || step.Observation.Results[0].Content != "evidence" {
		t.Fatalf("step=%+v", step)
	}
}

func TestExportContentProjectionGap(t *testing.T) {
	raw := json.RawMessage(`[{"type":"image","image":{"url":"https://example.test/no-type"}}]`)
	message, issues := rawToMessage(raw)
	if message != "" || len(issues) != 1 {
		t.Fatalf("message=%v issues=%v", message, issues)
	}
}

// +case=Anthropic cache reads and writes survive provider decoding, recording and ATIF export without omission or double counting
func TestExportAnthropicCacheUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg-cache","type":"message","role":"assistant","model":"test","content":[],"stop_reason":null,"usage":{"input_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

data: {"type":"message_stop"}

`)
	}))
	defer server.Close()
	review := recording.New(t.TempDir(), "main", "test", recording.SessionOptions{})
	defer review.Finalize()
	scope := review.GetOrCreateScope(recording.Scope{ID: "unit", Kind: "unit"})
	model := llm.NewModel(llm.ResolvedEndpoint{Protocol: "anthropic", URL: server.URL, Model: "test", Token: "test-token"})
	response, err := llm.Call(t.Context(), scope.RecordingModel(model, recording.PlanTask), agentgo.LLMRequest{Messages: []agentgo.Message{agentgo.UserMsg("review")}})
	if err != nil {
		t.Fatal(err)
	}
	usage := response.Message.Usage
	if usage == nil || usage.Input != 5 || usage.CacheWrite != 4 || usage.CacheRead != 3 || usage.Output != 1 {
		t.Fatalf("pinned provider usage = %+v", usage)
	}
	review.Finalize()
	path, err := review.TranscriptPath()
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := recording.ReadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := transcript.Costs().Total.Tokens.PromptTokens; got != 9 {
		t.Fatalf("recorded prompt tokens = %d, want 9", got)
	}
	trajectories, err := exportSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(trajectories) != 1 {
		t.Fatalf("trajectories = %d, want 1", len(trajectories))
	}
	totals := trajectories[0].FinalMetrics
	if totals.TotalPromptTokens != 9 || totals.TotalCompletionTokens != 1 || totals.TotalCachedTokens != 3 {
		t.Fatalf("exported usage = %+v", totals)
	}
}
