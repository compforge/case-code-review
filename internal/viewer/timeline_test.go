package viewer

import (
	"bytes"
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
		Operation: &timeline.OperationRecord{Revision: 1, Operation: "review", StartedAt: start, Status: timeline.Running},
		Stages:    []timeline.StageUpdate{{Revision: 1, Stage: timeline.Stage{ID: "wait-1", ParentID: "request-1", Name: "await_response", StartedAt: start, Status: timeline.Running}}},
	}
	update.Stages = append(update.Stages, timeline.StageUpdate{Revision: 1, Stage: timeline.Stage{ID: "request-1", ParentID: "operation:session-1", Name: "llm.request", StartedAt: start, Status: timeline.Running, Fields: map[string]json.RawMessage{"scope_id": json.RawMessage(`"unit-1"`), "execution_id": json.RawMessage(`"exec-1"`), "request_no": json.RawMessage(`1`), "task_type": json.RawMessage(`"main_task"`)}}})
	raw, err := json.Marshal(map[string]any{"type": "timeline_update", "execution_id": "exec-1", "scope_id": "unit-1", "taskType": "main_task", "request_no": 1, "timeline_id": "session-1", "update": update})
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
	if view.Timeline == nil || view.Timeline.ID != "session-1" {
		t.Fatal("missing run timeline")
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

func TestTimelineRendersHierarchyAndSourceIntervals(t *testing.T) {
	start := time.Now().Add(-time.Second)
	doc := &timeline.Document{ID: "run", RootStageID: "operation:run", OperationRecord: timeline.OperationRecord{StartedAt: start, Status: timeline.Running}, Stages: []timeline.StageUpdate{
		{Stage: timeline.Stage{ID: "child", ParentID: "parent", Name: "model.attempt", StartedAt: start.Add(10 * time.Millisecond), FinishedAt: start.Add(30 * time.Millisecond), Status: timeline.Succeeded}},
		{Stage: timeline.Stage{ID: "parent", ParentID: "operation:run", Name: "execution", StartedAt: start, Status: timeline.Running}},
	}}
	rows := timelineRows(doc)
	if len(rows) != 2 || rows[0].Name != "execution" || rows[1].DurationMS != 20 || rows[1].OffsetMS != 10 || rows[1].Label != "· model.attempt" {
		t.Fatalf("rows=%+v", rows)
	}
	tmpl, err := parseTemplate("session.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{"Session": &ViewSession{Timeline: doc}, "EncodedRepo": "repo", "RepoName": "repo"}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Run Timeline", "model.attempt", "incomplete"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing rendered %s", text)
		}
	}
}
