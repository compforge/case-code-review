package viewer

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
)

func TestViewerKeepsInterruptedRequestTimelineAndRequestIdentity(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	update := timeline.Update{
		Operation: &timeline.OperationRecord{Revision: 1, Operation: "llm.request", StartedAt: start, Status: timeline.Running},
		Stages:    []timeline.StageUpdate{{Revision: 1, Stage: timeline.Stage{ID: "wait-1", ParentID: "operation:request-1", Name: "await_response", StartedAt: start, Status: timeline.Running}}},
	}
	raw, err := json.Marshal(map[string]any{"type": "timeline_update", "execution_id": "exec-1", "scope_id": "unit-1", "taskType": "main_task", "request_no": 1, "timeline_id": "request-1", "update": update})
	if err != nil {
		t.Fatal(err)
	}
	writeViewerSession(t, root, "repo", "session-1", sessionStart("session-1"),
		`{"type":"execution_start","execution_id":"exec-1","scope_id":"unit-1","kind":"unit","scope":"file","taskType":"main_task"}`,
		`{"type":"llm_request","kind":"unit","scope":"file","filePath":"a.go","execution_id":"exec-1","scope_id":"unit-1","taskType":"main_task","request_no":1,"messages":[{"role":"user","content":"first"}]}`,
		`{"type":"llm_request","kind":"unit","scope":"file","filePath":"a.go","execution_id":"exec-1","scope_id":"unit-1","taskType":"main_task","request_no":2,"messages":[{"role":"user","content":"second"}]}`,
		string(raw),
	)
	view, err := LoadSession(root, "repo", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	execution := view.Reviews[0].Executions[0]
	if execution.Status != "incomplete" {
		t.Fatalf("execution status=%s", execution.Status)
	}
	cards := execution.Tasks[MainTask]
	if cards[0].Timeline == nil || cards[1].Timeline != nil {
		t.Fatal("timeline associated with wrong concurrent request")
	}
	if cards[0].Timeline.Status != timeline.Running {
		t.Fatalf("timeline=%+v", cards[0].Timeline)
	}
	nodes := buildConversation("exec-1", cards, nil)
	found := false
	for _, node := range nodes {
		if node.Kind == "timeline" && strings.Contains(node.Label, "Request Timeline") {
			found = true
		}
	}
	if !found {
		t.Fatal("timeline is not visible in conversation")
	}
}
