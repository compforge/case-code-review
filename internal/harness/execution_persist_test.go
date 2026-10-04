package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"

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
		Messages:  []agentgo.AgentMessage{msg.Text("user", "review this unit")},
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

	transcript, err := session.ReadTranscript(file)
	if err != nil {
		t.Fatal(err)
	}
	facts := transcript.ExecutionFacts()
	if len(facts) != 1 || facts[0]["outcome"] != string(OutcomeCompleted) {
		t.Fatalf("execution facts: %+v", facts)
	}
	stages := transcript.StageIndex()
	seen := map[string]int{}
	keptToolInvocations := false
	for _, record := range transcript.Records {
		kind, _ := record["type"].(string)
		if kind == "execution_start" || kind == "execution_end" {
			t.Fatal("duplicate lifecycle record", kind)
		}
		if kind != "llm_request" && kind != "llm_response" && kind != "tool_result" {
			continue
		}
		if record["execution_id"] != result.ID {
			t.Fatal("lost execution identity", record)
		}
		id, _ := record["stage_id"].(string)
		if _, ok := stages[timeline.StageID(id)]; !ok {
			t.Fatal("missing associated stage", record)
		}
		if _, ok := record["duration_ms"]; ok {
			t.Fatal("duplicate timing", record)
		}
		if kind == "llm_request" {
			for _, raw := range record["messages"].([]any) {
				message := raw.(map[string]any)
				if calls, ok := message["tool_calls"].([]any); ok && len(calls) > 0 {
					keptToolInvocations = true
				}
			}
		}
		seen[kind]++
	}
	if !keptToolInvocations {
		t.Fatal("prompt snapshot dropped assistant tool invocations")
	}
	for _, kind := range []string{"llm_request", "llm_response", "tool_result"} {
		if seen[kind] == 0 {
			t.Fatal("missing", kind)
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
