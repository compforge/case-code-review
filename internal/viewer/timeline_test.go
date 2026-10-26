package viewer

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

func TestViewerKeepsInterruptedRequestTimelineAndRequestIdentity(t *testing.T) {
	root := t.TempDir()
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	snapshot := operation.Snapshot{
		ID: "session-1", RootID: "operation:session-1", Operation: "review", StartedAt: start, Status: operation.Unknown,
		Operations: []operation.Record{
			{ID: "wait-1", ParentID: "request-1", Name: "await_response", StartedAt: start, Status: operation.Running},
			{ID: "exec-1", ParentID: "operation:session-1", Name: "execution", StartedAt: start, Status: operation.Running, Attributes: map[string]json.RawMessage{"execution_id": json.RawMessage(`"exec-1"`), "scope_id": json.RawMessage(`"unit-1"`), "kind": json.RawMessage(`"unit"`), "scope": json.RawMessage(`"file"`), "task_type": json.RawMessage(`"main_task"`)}},
		},
	}
	snapshot.Operations = append(snapshot.Operations, operation.Record{ID: "request-1", ParentID: "exec-1", Name: "llm.request", StartedAt: start, Status: operation.Running, Attributes: map[string]json.RawMessage{"scope_id": json.RawMessage(`"unit-1"`), "execution_id": json.RawMessage(`"exec-1"`), "request_no": json.RawMessage(`1`), "task_type": json.RawMessage(`"main_task"`)}})
	var encoded []string
	for _, op := range snapshot.Operations {
		raw, err := json.Marshal(map[string]any{"type": "operation", "review_id": "session-1", "operation": op})
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, string(raw))
	}
	writeViewerSession(t, root, "repo", "session-1", sessionStart("session-1"),
		`{"type":"llm_request","kind":"unit","scope":"file","filePath":"a.go","execution_id":"exec-1","scope_id":"unit-1","taskType":"main_task","request_no":1,"messages":[{"role":"user","content":"first"}],"operation_id":"request-1","review_id":"session-1"}`,
		`{"type":"llm_request","kind":"unit","scope":"file","filePath":"a.go","execution_id":"exec-1","scope_id":"unit-1","taskType":"main_task","request_no":2,"messages":[{"role":"user","content":"second"}],"operation_id":"request-2","review_id":"session-1"}`,
		strings.Join(encoded, "\n"),
	)
	view, err := LoadSession(root, "repo", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if view.Journal == nil || view.Journal.ID != "session-1" {
		t.Fatal("missing run timeline")
	}
	if view.Summary.Completion.Status != "running" {
		t.Fatalf("completion = %s", view.Summary.Completion.Status)
	}
	if len(view.Journal.Operations) != 3 {
		t.Fatalf("lost interrupted stages: %+v", view.Journal)
	}

}

func TestTimelineRendersHierarchyAndSourceIntervals(t *testing.T) {
	start := time.Now().Add(-time.Second)
	doc := &operation.Snapshot{ID: "run", RootID: "operation:run", StartedAt: start, Status: operation.Unknown, Operations: []operation.Record{
		{ID: "child", ParentID: "parent", Name: "model.attempt", StartedAt: start.Add(10 * time.Millisecond), FinishedAt: start.Add(30 * time.Millisecond), Status: operation.Succeeded, Attributes: map[string]json.RawMessage{"attempt": json.RawMessage(`3`)}},
		{ID: "parent", ParentID: "operation:run", Name: "execution", StartedAt: start, Status: operation.Running},
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
	if err := tmpl.Execute(&out, map[string]any{"Session": &ViewSession{Journal: doc}, "EncodedRepo": "repo", "RepoName": "repo"}); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Review Timeline", "Model call", "incomplete", "attempt:</strong> <code>3</code>", `<dialog class="stage-dialog"`, `data-stage-detail="child"`} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing rendered %s", text)
		}
	}
}

func TestEmptyParentIsIndependentRatherThanMissing(t *testing.T) {
	start := time.Now()
	doc := &operation.Snapshot{ID: "run", StartedAt: start, Operations: []operation.Record{
		{ID: "top", Name: "work", StartedAt: start},
		{ID: "orphan", ParentID: "absent", Name: "remote work", StartedAt: start},
	}}
	rows := timelineRows(doc)
	for _, row := range rows {
		if row.MissingParent != (row.ID == "orphan") || row.Depth != 0 {
			t.Fatalf("incorrect parent semantics: %+v", row)
		}
	}
}

func TestTimelineWaterfallPreservesParallelIntervalsAndUnknownEnds(t *testing.T) {
	start := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	doc := &operation.Snapshot{RootID: "root", StartedAt: start, FinishedAt: start.Add(10 * time.Second), Operations: []operation.Record{
		{ID: "a", ParentID: "root", Name: "parallel-a", StartedAt: start.Add(2 * time.Second), FinishedAt: start.Add(6 * time.Second)},
		{ID: "b", ParentID: "root", Name: "parallel-b", StartedAt: start.Add(2 * time.Second), FinishedAt: start.Add(6 * time.Second)},
		{ID: "unknown", ParentID: "missing", Name: "await_response", StartedAt: start.Add(9 * time.Second), Status: operation.Running},
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
	if view.Rows[0].DurationMS != 4000 {
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

// +case:id=timeline_stage_session_link,desc=`a child model or tool stage inherits the enclosing loop reference`,expect=`Stage details show the local Session ID and link to the matching execution, without inventing associations for preparation stages`
func TestTimelineStageInheritsSessionLink(t *testing.T) {
	doc := &operation.Snapshot{RootID: "root", Operations: []operation.Record{
		{ID: "prepare", ParentID: "root", Name: "diff.load"},
		{ID: "loop", ParentID: "root", Name: "execution", Attributes: map[string]json.RawMessage{
			"scope_id": json.RawMessage(`"unit-a"`), "session_id": json.RawMessage(`"local-session"`), "execution_id": json.RawMessage(`"exec-a"`),
		}},
		{ID: "model", ParentID: "loop", Name: "model.attempt"},
	}}
	for _, row := range timelineRows(doc) {
		if row.ID == "prepare" && row.SessionID != "" {
			t.Fatal("preparation stage acquired a Session")
		}
		if row.ID == "model" && (row.SessionID != "local-session" || row.ScopeID != "unit-a" || row.ExecutionID != "exec-a") {
			t.Fatalf("association=%+v", row)
		}
	}
	tmpl, err := parseTemplate("session.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, sessionPageData{Session: &ViewSession{Journal: doc}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `Open full Session`) || !strings.Contains(out.String(), `<dt>Session ID:</dt><dd><code>local-session</code>`) {
		t.Fatal("Stage details lost the Session reference")
	}
}

// +case:id=review_loop_parallel_lanes,desc=`R1 Units and R2 Lanes overlap, a lane executes again after a gap, and one loop has no end`,expect=`shared positions preserve overlap and gaps; the unfinished loop has no fabricated width`
func TestReviewLoopLanesPreserveConcurrencyAndGaps(t *testing.T) {
	start := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	stage := func(id, task, session string, from, to int) operation.Record {
		s := operation.Record{ID: operation.ID(id), ParentID: "root", Name: "execution", StartedAt: start.Add(time.Duration(from) * time.Second), Status: operation.Succeeded,
			Attributes: map[string]json.RawMessage{"task_type": json.RawMessage(`"` + task + `"`), "session_id": json.RawMessage(`"` + session + `"`), "scope_id": json.RawMessage(`"` + session + `"`)}}
		if to >= 0 {
			s.FinishedAt = start.Add(time.Duration(to) * time.Second)
		} else {
			s.Status = operation.Running
		}
		return s
	}
	doc := &operation.Snapshot{RootID: "root", StartedAt: start, FinishedAt: start.Add(10 * time.Second), Operations: []operation.Record{
		stage("unit-a", string(recording.MainTask), "a", 0, 6),
		stage("unit-b", string(recording.MainTask), "b", 1, 7),
		stage("lane-c-first", string(recording.HypothesisReviewTask), "c", 3, 5),
		stage("lane-d", string(recording.HypothesisReviewTask), "d", 4, 8),
		stage("lane-c-second", string(recording.HypothesisReviewTask), "c", 7, 9),
		stage("unit-open", string(recording.MainTask), "open", 9, -1),
	}}
	view := layoutTimeline(doc)
	if len(view.Lanes) != 5 {
		t.Fatalf("lanes=%+v", view.Lanes)
	}
	lanes := map[string]timelineLane{}
	for _, lane := range view.Lanes {
		lanes[lane.Label] = lane
	}
	a, b, c, d := lanes["a"].Bars[0], lanes["b"].Bars[0], lanes["c"].Bars, lanes["d"].Bars[0]
	if a.StartPercent != 0 || a.WidthPercent != 60 || b.StartPercent != 10 || b.WidthPercent != 60 {
		t.Fatal("R1 overlap lost")
	}
	if len(c) != 2 || c[0].StartPercent != 30 || c[0].WidthPercent != 20 || c[1].StartPercent != 70 || c[1].WidthPercent != 20 || d.StartPercent != 40 || d.WidthPercent != 40 {
		t.Fatal("R2 overlap or idle gap lost")
	}
	if open := lanes["open"].Bars[0]; open.WidthPercent != 0 || open.StartPercent != 90 {
		t.Fatalf("unknown end painted as duration: %+v", open)
	}
	tmpl, err := parseTemplate("session.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, sessionPageData{Session: &ViewSession{Journal: doc}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `data-timeline-stage="lane-c-second"`) || strings.Contains(out.String(), "ZgotmplZ") {
		t.Fatal("concurrency bar lost its safe Stage target")
	}
}

func TestSwimlanesKeepPreparationNestedWorkAndTrial(t *testing.T) {
	start := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	stage := func(id, parent, name string, from, to int) operation.Record {
		return operation.Record{ID: operation.ID(id), ParentID: operation.ID(parent), Name: name, StartedAt: start.Add(time.Duration(from) * time.Second), FinishedAt: start.Add(time.Duration(to) * time.Second)}
	}
	loop := stage("loop", "root", "execution", 2, 8)
	loop.Attributes = map[string]json.RawMessage{"task_type": json.RawMessage(`"main_task"`), "session_id": json.RawMessage(`"session-a"`)}
	doc := &operation.Snapshot{RootID: "root", StartedAt: start, FinishedAt: start.Add(10 * time.Second), Operations: []operation.Record{
		stage("diff", "root", "diff.load", 0, 2), stage("graph", "root", "codegraph.build", 1, 3),
		loop, stage("model", "loop", "model.attempt", 3, 6), stage("tool", "loop", "tool.invoke", 6, 7),
		stage("trial", "root", "trial.assess", 7, 9), stage("unknown", "absent", "custom.stage", 9, 10),
	}}
	view := layoutTimeline(doc)
	seen := map[operation.ID]int{}
	phases := map[string]timelineLane{}
	for _, lane := range view.Lanes {
		phases[lane.Phase] = lane
		for _, row := range lane.Children {
			seen[row.ID]++
		}
		for _, track := range lane.Tracks {
			for _, row := range track {
				seen[row.ID]++
			}
		}
	}
	for _, stage := range doc.Operations {
		if seen[stage.ID] != 1 {
			t.Fatalf("stage %s rendered %d times", stage.ID, seen[stage.ID])
		}
	}
	if len(phases["Prepare"].Tracks) != 2 || len(phases["R1"].Children) != 2 || len(phases["R3"].Bars) != 1 {
		t.Fatalf("lane ownership or overlap lost: %+v", view.Lanes)
	}
	tmpl, err := parseTemplate("session.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, sessionPageData{Session: &ViewSession{Journal: doc}}); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, stage := range doc.Operations {
		if !strings.Contains(body, `data-stage-detail="`+string(stage.ID)+`"`) {
			t.Fatalf("missing dialog content for %s", stage.ID)
		}
	}
	if strings.Contains(body, "Review details</") || strings.Contains(body, "Stage tree") {
		t.Fatal("legacy navigation remains")
	}
	// This fixture has no scope_id; show the Session ID without inventing a link.
	if !strings.Contains(body, `data-stage-close`) || strings.Contains(body, `data-timeline-execution="loop"`) {
		t.Fatal("invalid modal controls or fabricated Session association")
	}
}

// +case=`Nested stages follow ParentID, sibling turns remain separate, and Unit pipelines are not presented as an extra review phase`
func TestSwimlaneTurnHierarchy(t *testing.T) {
	start := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	stage := func(id, parent, name string) operation.Record {
		return operation.Record{ID: operation.ID(id), ParentID: operation.ID(parent), Name: name, StartedAt: start, FinishedAt: start.Add(time.Second)}
	}
	pipeline := stage("pipeline", "root", "review.pipeline")
	pipeline.Attributes = map[string]json.RawMessage{"unit_id": json.RawMessage(`"unit-a"`)}
	loop := stage("loop", "pipeline", "execution")
	loop.Attributes = map[string]json.RawMessage{"task_type": json.RawMessage(`"main_task"`)}
	turn := stage("turn-1", "loop", "turn")
	turn.Attributes = map[string]json.RawMessage{"turn": json.RawMessage(`1`)}
	doc := &operation.Snapshot{RootID: "root", StartedAt: start, FinishedAt: start.Add(time.Second), Operations: []operation.Record{
		stage("http", "provider", "http.request"), stage("turn-2", "loop", "turn"),
		pipeline, loop, turn, stage("model", "turn-1", "model.attempt"),
		stage("provider", "model", "llm.provider"), stage("context", "turn-1", "context.project"),
		stage("tool", "turn-1", "tool.execution"), stage("invoke", "tool", "tool.invoke"),
	}}
	view := layoutTimeline(doc)
	var r1, unitLane timelineLane
	for _, lane := range view.Lanes {
		switch lane.Phase {
		case "R1":
			r1 = lane
		case "Unit":
			unitLane = lane
		}
	}
	if len(r1.Tree) != 2 || r1.Tree[0].ID != "turn-1" || r1.Tree[1].ID != "turn-2" {
		t.Fatalf("turns must be sibling roots: %+v", r1.Tree)
	}
	first := r1.Tree[0]
	if first.Label() != "turn 1" || first.Depth != 0 || len(first.Children) != 3 {
		t.Fatalf("turn children lost: %+v", first)
	}
	model := first.Children[1]
	if model.ID != "model" || model.Depth != 1 || len(model.Children) != 1 || model.Children[0].ID != "provider" || model.Children[0].Children[0].ID != "http" {
		t.Fatalf("model/provider/HTTP hierarchy lost: %+v", model)
	}
	tool := first.Children[2]
	if tool.ID != "tool" || len(tool.Children) != 1 || tool.Children[0].ID != "invoke" {
		t.Fatalf("tool invocation parent lost: %+v", tool)
	}
	if len(unitLane.Bars) != 1 || unitLane.Bars[0].Label() != "Pipeline · unit-a" {
		t.Fatalf("Unit pipeline identity lost: %+v", unitLane)
	}
	tmpl, err := parseTemplate("session.html")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, sessionPageData{Session: &ViewSession{Journal: doc}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"turn-1", "model", "provider", "tool"} {
		if !strings.Contains(out.String(), `<details class="stage-branch" data-stage-branch="`+id+`">`) {
			t.Fatalf("stage %s must start collapsed", id)
		}
	}
	if strings.Contains(out.String(), "Coordination") || strings.Contains(out.String(), "Review results and statistics") {
		t.Fatal("obsolete review presentation remains")
	}
}
