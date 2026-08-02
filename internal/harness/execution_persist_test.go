package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
	var executionEnds int
	seen := map[string]int{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var record map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		recordType, _ := record["type"].(string)
		if recordType != "llm_request" && recordType != "llm_response" && recordType != "tool_call" && recordType != "execution_end" {
			continue
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
	for _, recordType := range []string{"llm_request", "llm_response", "tool_call"} {
		if seen[recordType] == 0 {
			t.Fatalf("missing %s record: %+v", recordType, seen)
		}
	}
}
