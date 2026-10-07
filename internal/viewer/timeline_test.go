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
	update.Stages = append(update.Stages, timeline.StageUpdate{Revision: 1, Stage: timeline.Stage{ID: "request-1", ParentID: "operation:session-1", Name: "llm.request", StartedAt: start, Status: timeline.Running, Attributes: map[string]json.RawMessage{"scope_id": json.RawMessage(`"unit-1"`), "execution_id": json.RawMessage(`"exec-1"`), "request_no": json.RawMessage(`1`), "task_type": json.RawMessage(`"main_task"`)}}})
	raw, err := json.Marshal(map[string]any{"type": "timeline_update", "execution_id": "exec-1", "scope_id": "unit-1", "taskType": "main_task", "request_no": 1, "timeline_id": "session-1", "update": update})
	if err != nil {
		t.Fatal(err)
	}
	writeViewerSession(t, root, "repo", "session-1", sessionStart("session-1"),
		`{"type":"timeline_update","timeline_id":"session-1","update":{"Stages":[{"revision":1,"id":"exec-1","parent_id":"operation:session-1","name":"execution","started_at":"2026-08-02T00:00:00+00:00","status":"running","fields":{"execution_id":"exec-1","scope_id":"unit-1","kind":"unit","scope":"file","task_type":"main_task"}}]}}`,
		`{"type":"llm_request","kind":"unit","scope":"file","filePath":"a.go","execution_id":"exec-1","scope_id":"unit-1","taskType":"main_task","request_no":1,"messages":[{"role":"user","content":"first"}],"stage_id":"request-1","timeline_id":"session-1"}`,
		`{"type":"llm_request","kind":"unit","scope":"file","filePath":"a.go","execution_id":"exec-1","scope_id":"unit-1","taskType":"main_task","request_no":2,"messages":[{"role":"user","content":"second"}],"stage_id":"request-2","timeline_id":"session-1"}`,
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
		{Stage: timeline.Stage{ID: "child", ParentID: "parent", Name: "model.attempt", StartedAt: start.Add(10 * time.Millisecond), FinishedAt: start.Add(30 * time.Millisecond), Status: timeline.Succeeded, Attributes: map[string]json.RawMessage{"attempt": json.RawMessage(`3`)}}},
		{Stage: timeline.Stage{ID: "parent", ParentID: "operation:run", Name: "execution", StartedAt: start, Status: timeline.Running}},
	}}
	rows := timelineRows(doc)
	if len(rows) != 2 || rows[0].Name != "execution" || rows[1].DurationMS != 20 || rows[1].OffsetMS != 10 || rows[1].Depth != 1 || !rows[0].HasChildren {
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
	for _, text := range []string{"Run Timeline", "model.attempt", "incomplete", "attempt:</strong> <code>3</code>", `role="tabpanel"`, `aria-controls="panel-timeline"`} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing rendered %s", text)
		}
	}
}

func TestTimelineWaterfallPreservesParallelIntervalsAndUnknownEnds(t *testing.T) {
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	doc := &timeline.Document{RootStageID: "root", OperationRecord: timeline.OperationRecord{StartedAt: start, FinishedAt: start.Add(10 * time.Second)}, Stages: []timeline.StageUpdate{
		{Stage: timeline.Stage{ID: "a", ParentID: "root", Name: "parallel-a", StartedAt: start.Add(2 * time.Second), FinishedAt: start.Add(6 * time.Second), Elapsed: 3 * time.Second}},
		{Stage: timeline.Stage{ID: "b", ParentID: "root", Name: "parallel-b", StartedAt: start.Add(2 * time.Second), FinishedAt: start.Add(6 * time.Second)}},
		{Stage: timeline.Stage{ID: "unknown", ParentID: "missing", Name: "await_response", StartedAt: start.Add(9 * time.Second), Status: timeline.Running}},
	}}
	view := layoutTimeline(doc)
	if view.WindowMS != 10000 || view.Incomplete != 1 {
		t.Fatalf("view=%+v", view)
	}
	for _, row := range view.Rows[:2] {
		if row.StartPercent != 20 || row.WidthPercent != 40 {
			t.Fatalf("parallel interval changed: %+v", row)
		}
	}
	if view.Rows[0].DurationMS != 3000 {
		t.Fatal("elapsed label must preserve source duration")
	}
	if row := view.Rows[2]; row.WidthPercent != 0 || row.StartPercent != 90 || !row.MissingParent {
		t.Fatalf("unknown end fabricated: %+v", row)
	}
	doc.FinishedAt = time.Time{}
	if got := layoutTimeline(doc).WindowMS; got != 9000 {
		t.Fatalf("unclosed timeline should end at last evidence, got %d", got)
	}
}
