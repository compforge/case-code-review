package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestExecutionPersistsOneLifecycleAcrossModelAndToolRecords(t *testing.T) {
	session.UseTestSessions()
	home := t.TempDir()
	t.Setenv("HOME", home)

	history := session.New(filepath.Join(home, "repo"), "main", "review-model", session.SessionOptions{})
	scope := session.Scope{ID: "unit-1", Kind: "unit", Type: "func", Paths: []string{"a.go"}}
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("comment-1", "code_comment", `{"path":"a.go"}`, nil),
		toolCallResponseID("done-1", "task_done", `{}`, nil),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
		LLMClient: client,
		Messages:  []msg.Msg{msg.Text("user", "review this unit")},
		ToolDefs:  []llm.ToolDef{toolDef("code_comment"), toolDef("task_done")},
		ToolHandler: toolHandlerFunc(func(_ context.Context, request ToolRequest) (tool.TaskCheckpoint, bool) {
			if request.Tool.Name() != "code_comment" {
				return tool.TaskCheckpoint{}, false
			}
			return tool.Of("submitted"), true
		}),
		Session:  history,
		Scope:    scope,
		MaxTurns: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != OutcomeCompleted {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.ID == "" || result.Duration <= 0 {
		t.Fatalf("execution identity/timing missing: %+v", result)
	}
	history.Finalize()

	paths, err := filepath.Glob(filepath.Join(home, ".casecodereview", "test-sessions", "*", history.SessionID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 {
		t.Fatalf("session files = %v, want one", paths)
	}
	file, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	var executionID string
	var executionStarts int
	var executionEnds int
	seen := map[string]int{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		recordType, _ := record["type"].(string)
		if recordType != "execution_start" && recordType != "llm_request" && recordType != "llm_response" && recordType != "tool_call" && recordType != "execution_end" {
			continue
		}
		if _, ok := record["elapsed_ms"].(float64); !ok {
			t.Fatalf("%s record has no elapsed_ms: %+v", recordType, record)
		}
		id, _ := record["execution_id"].(string)
		if id == "" {
			t.Fatalf("%s record has no execution_id: %+v", recordType, record)
		}
		if executionID == "" {
			executionID = id
		} else if id != executionID {
			t.Fatalf("execution_id = %q, want %q for %s", id, executionID, recordType)
		}
		seen[recordType]++
		if recordType == "execution_start" {
			executionStarts++
			if seen["llm_request"] != 0 {
				t.Fatalf("execution_start appeared after llm_request: %+v", seen)
			}
		}
		if recordType == "execution_end" {
			executionEnds++
			if record["outcome"] != string(OutcomeCompleted) || record["taskType"] != string(session.MainTask) {
				t.Fatalf("unexpected execution_end: %+v", record)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if executionEnds != 1 {
		t.Fatalf("execution_end records = %d, want 1", executionEnds)
	}
	if executionStarts != 1 {
		t.Fatalf("execution_start records = %d, want 1", executionStarts)
	}
	for _, recordType := range []string{"llm_request", "llm_response", "tool_call"} {
		if seen[recordType] == 0 {
			t.Fatalf("missing %s record: %+v", recordType, seen)
		}
	}
}

func TestContextCompactionEventPersistsWithExecutionIdentity(t *testing.T) {
	session.UseTestSessions()
	home := t.TempDir()
	t.Setenv("HOME", home)

	history := session.New(filepath.Join(home, "repo"), "main", "review-model", session.SessionOptions{})
	scope := session.Scope{ID: "unit-compact", Kind: "unit", Type: "func", Paths: []string{"a.go"}}
	recorder := newExecutionRecorder(ExecutionSpec{Session: history, Scope: scope, TaskType: session.MainTask}, "exec-compact")
	var emitted ExecutionEvent
	emitExecutionEvent(EventSinkFunc(func(event ExecutionEvent) { emitted = event }), recorder, agentgo.Event{
		Type: agentgo.EventContextCompacted,
		Compaction: &agentgo.CompactionInfo{
			Reason: agentgo.CompactReasonThreshold, Committed: true,
			TokensBefore: 1000, TokensAfter: 600,
			MessagesBefore: 8, MessagesAfter: 5, Summarized: true,
		},
	})
	history.Finalize()

	if emitted.Type != EventContextCompacted || emitted.Compaction == nil || emitted.Compaction.TokensAfter != 600 {
		t.Fatalf("unexpected Harness event: %+v", emitted)
	}
	paths, err := filepath.Glob(filepath.Join(home, ".casecodereview", "test-sessions", "*", history.SessionID+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("session files = %v err=%v", paths, err)
	}
	file, err := os.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	found := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record["type"] != "context_compacted" {
			continue
		}
		found = true
		if record["execution_id"] != "exec-compact" || record["taskType"] != string(session.MainTask) ||
			record["reason"] != "threshold" || record["committed"] != true ||
			record["tokens_before"].(float64) != 1000 || record["tokens_after"].(float64) != 600 ||
			record["messages_before"].(float64) != 8 || record["messages_after"].(float64) != 5 ||
			record["summarized"] != true {
			t.Fatalf("unexpected context_compacted record: %+v", record)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("context_compacted record was not persisted")
	}
}
