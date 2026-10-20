package harness

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
)

func TestExecutionPersistsOneLifecycleAcrossModelAndToolRecords(t *testing.T) {
	recording.UseTestSessions()
	home := t.TempDir()
	t.Setenv("HOME", home)

	history := recording.New(filepath.Join(home, "repo"), "main", "review-model", recording.SessionOptions{})
	scope := recording.Scope{ID: "unit-1", Kind: "unit", Type: "func", Paths: []string{"a.go"}}
	client := &scriptedClient{responses: []*agentgo.LLMResponse{
		toolCallResponseID("comment-1", "code_comment", `{"path":"a.go"}`, nil),
		toolCallResponseID("done-1", "task_done", `{}`, nil),
	}}

	result, err := runExecution(context.Background(), ExecutionSpec{
		ChatModel: client,
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

	transcript, err := recording.ReadReview(file.Name())
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
				if blocks, ok := message["content"].([]any); ok {
					for _, raw := range blocks {
						block := raw.(map[string]any)
						if block["tool_call"] != nil {
							keptToolInvocations = true
						}
					}
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

func TestContextCompactionEventForwardsToUI(t *testing.T) {
	recording.UseTestSessions()
	home := t.TempDir()
	t.Setenv("HOME", home)

	history := recording.New(filepath.Join(home, "repo"), "main", "review-model", recording.SessionOptions{})
	scope := recording.Scope{ID: "unit-compact", Kind: "unit", Type: "func", Paths: []string{"a.go"}}
	recorder := newExecutionRecorder(ExecutionSpec{Session: history, Scope: scope, TaskType: recording.MainTask}, "exec-compact")
	var emitted ExecutionEvent
	emitExecutionEvent(EventSinkFunc(func(event ExecutionEvent) { emitted = event }), recorder, agentgo.Event{
		Type: agentgo.EventContextCompacted,
		Compaction: &agentgo.CompactionInfo{
			Reason: agentgo.CompactReasonThreshold, Committed: true,
			TokensBefore: 1000, TokensAfter: 600,
			MessagesBefore: 8, MessagesAfter: 5, Summarized: true,
		},
	}, recorder.executionID)
	history.Finalize()

	if emitted.Type != EventContextCompacted || emitted.Compaction == nil || emitted.Compaction.TokensAfter != 600 {
		t.Fatalf("unexpected Harness event: %+v", emitted)
	}
}
