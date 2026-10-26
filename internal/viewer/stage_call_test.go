package viewer

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// +case=`Tool detail joins a single Session/run/execution/attempt, prefers actual invocation input, and keeps missing results explicit`
func TestStageToolCall(t *testing.T) {
	root := t.TempDir()
	writeViewerSession(t, root, "repo", "review", sessionStart("review"))
	base := filepath.Join(root, "repo", "review")
	at := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	ref := agentsession.Ref{SessionID: "local", RunID: "run-1", ExecutionID: "same-call", Attempt: 2}
	rawRef, _ := json.Marshal(ref)
	stages := []operation.Record{
		{ID: "execution", Name: "execution", StartedAt: at, Attributes: map[string]json.RawMessage{"session_file": json.RawMessage(`"loop.jsonl"`)}},
		{ID: "invoke", ParentID: "execution", Name: "tool.invoke", StartedAt: at, Attributes: map[string]json.RawMessage{"session_ref": rawRef}},
	}
	writeTimeline := func() {
		t.Helper()
		writeStageAgentFixture(t, base+".jsonl", stages)
	}
	writeTimeline()
	if err := os.Mkdir(base+".sessions", 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(filepath.Join(base+".sessions", "loop.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	seq := uint64(0)
	appendRecord := func(kind string, run string, attempt int, data any) {
		t.Helper()
		seq++
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		record := agentsession.Record{Type: kind, Seq: seq, SessionID: "local", RunID: run, Timestamp: at, Data: raw}
		if attempt > 0 {
			record.Execution = &agentgo.Execution{ID: "same-call", Attempt: attempt}
		}
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	appendRecord(agentsession.SessionStart, "", 0, map[string]int{"version": 1})
	for _, run := range []string{"run-1", "run-2"} {
		for attempt := 1; attempt <= 2; attempt++ {
			appendRecord(string(agentgo.EventToolExecStart), run, attempt, agentsession.EventData{Args: json.RawMessage(`{"original":true}`)})
			appendRecord(string(agentgo.EventToolInvokeStart), run, attempt, agentsession.EventData{Tool: "search_code", Args: json.RawMessage(`{"query":"<script>test</script>"}`)})
			appendRecord(string(agentgo.EventToolExecEnd), run, attempt, agentsession.EventData{Tool: "search_code", ToolResult: &agentsession.ToolResultData{Content: json.RawMessage(`"` + run + `"`), Details: map[string]int{"attempt": attempt}}, IsError: run == "run-1" && attempt == 2, Error: "tool diagnostic"})
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// Unrelated Sessions must not be opened by a single-call request.
	if err := os.WriteFile(filepath.Join(base+".sessions", "unrelated.jsonl"), []byte("corrupt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call, err := loadStageCall(base+".jsonl", "invoke")
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]string
	if err := json.Unmarshal(call.Input, &input); err != nil {
		t.Fatal(err)
	}
	if call.Ref != ref || input["query"] != "<script>test</script>" || call.Output == nil || string(call.Output.Content) != `"run-1"` || call.Output.Details.(map[string]any)["attempt"] != float64(2) || !call.IsError || call.Error != "tool diagnostic" || len(call.Warnings) > 0 {
		t.Fatalf("wrong call data: %+v", call)
	}
	request := httptest.NewRequest("GET", "/call?stage=invoke", nil)
	response := httptest.NewRecorder()
	handleStageCall(response, request, root, "repo", "review")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"tool":"search_code"`) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("response: %s", response.Body.String())
	}

	// Same call ID in another Session or attempt must not supply missing content.
	for _, missing := range []agentsession.Ref{
		{SessionID: "other", RunID: "run-1", ExecutionID: "same-call", Attempt: 2},
		{SessionID: "local", RunID: "run-1", ExecutionID: "same-call", Attempt: 3},
	} {
		stages[1].Attributes["session_ref"], _ = json.Marshal(missing)
		writeTimeline()
		call, err := loadStageCall(base+".jsonl", "invoke")
		if err != nil || call.Input != nil || call.Output != nil {
			t.Fatalf("missing call borrowed data: %+v, %v", call, err)
		}
	}
	stages[0].Attributes["session_file"] = json.RawMessage(`"../outside.jsonl"`)
	writeTimeline()
	if _, err := loadStageCall(base+".jsonl", "invoke"); err == nil {
		t.Fatal("accepted traversal in Session filename")
	}
	response = httptest.NewRecorder()
	handleStageCall(response, request, root, "../repo", "review")
	if response.Code != 400 {
		t.Fatalf("path traversal status: %d", response.Code)
	}
}

// +case=`Model detail preserves the exact request, reasoning, tool calls and usage for one physical attempt; failed attempts do not inherit a later response`
func TestStageModelCall(t *testing.T) {
	path := "../../testdata/recording/review.jsonl"
	failed, err := loadStageCall(path, "run-1/model/model-1/1")
	if err != nil {
		t.Fatal(err)
	}
	if failed.Kind != "model" || failed.Request == nil || failed.Response != nil || failed.Error != "temporary provider failure" {
		t.Fatalf("failed attempt lost request or borrowed response: %+v", failed)
	}
	success, err := loadStageCall(path, "run-1/model/model-1/2")
	if err != nil {
		t.Fatal(err)
	}
	if success.Request == nil || len(success.Request.Messages) != 1 || success.Request.Messages[0].TextContent() != "inspect caller" || success.Response == nil {
		t.Fatalf("missing model request/response: %+v", success)
	}
	response := success.Response
	if response.TextContent() != "read evidence" || response.ThinkingContent() != "check the guard" || len(response.ToolCalls()) != 1 || response.ToolCalls()[0].Name != "read_files" || response.Usage == nil || response.Usage.Input != 100 {
		t.Fatalf("response facts lost: %+v", response)
	}
	secondRun, err := loadStageCall(path, "run-2/model/model-1/1")
	if err != nil {
		t.Fatal(err)
	}
	if secondRun.Ref.RunID == success.Ref.RunID || secondRun.Response == nil || secondRun.Error != "" {
		t.Fatalf("cross-run association: %+v", secondRun)
	}
	// Both model.attempt and direct llm.request are call boundaries.
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "review.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), `"name":"model.attempt"`, `"name":"llm.request"`))
	if err := os.WriteFile(filepath.Join(root, "review.jsonl"), data, 0600); err != nil {
		t.Fatal(err)
	}
	direct, err := loadStageCall(filepath.Join(root, "review.jsonl"), "run-1/model/model-1/2")
	if err != nil || direct.Response == nil || direct.Response.TextContent() != response.TextContent() {
		t.Fatalf("direct model call: %+v %v", direct, err)
	}
}

// Failed responses are inspection evidence even though AgentGo excludes them from replay.
func TestStageModelCallKeepsFailedResponseUsage(t *testing.T) {
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "review.sessions", "loop.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), `"stop_reason":"tool_use"`, `"stop_reason":"error"`))
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	call, err := loadStageCall(filepath.Join(root, "review.jsonl"), "run-1/model/model-1/2")
	if err != nil || call.Response == nil || call.Response.StopReason != agentgo.StopReasonError || call.Response.Usage == nil || call.Response.Usage.Input != 100 {
		t.Fatalf("failed response evidence lost: %+v %v", call, err)
	}
}
