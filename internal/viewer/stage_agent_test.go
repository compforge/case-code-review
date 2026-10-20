package viewer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/compforge/agentgo"
	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/go-stdx/timeline/store"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
)

// Stage boundaries are added to the shared Session fixture without inventing
// events or changing its failed/successful attempt and cross-run identities.
func stageAgentFixture(t *testing.T) (string, []timeline.Stage) {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "review.jsonl")
	source, err := readStageSource(path)
	if err != nil {
		t.Fatal(err)
	}
	stages := source.rows
	at := source.byID["execution-1"].StartedAt
	for i := range stages {
		if stages[i].ParentID == "execution-1" {
			stages[i].ParentID = "run-1/turn/1"
		}
		if stages[i].ParentID == "execution-2" {
			stages[i].ParentID = "run-2/turn/1"
		}
	}
	for i, execution := range []timeline.StageID{"execution-1", "execution-2"} {
		turnID := []timeline.StageID{"run-1/turn/1", "run-2/turn/1"}[i]
		stages = append(stages, timeline.Stage{ID: turnID, ParentID: execution, Name: "turn", StartedAt: at, FinishedAt: at.Add(10 * time.Second), Status: timeline.Succeeded, Attributes: map[string]json.RawMessage{"turn": json.RawMessage(`1`)}})
	}
	writeStageAgentFixture(t, path, stages)
	return path, stages
}

func writeStageAgentFixture(t *testing.T, path string, stages []timeline.Stage) {
	t.Helper()
	updates := make([]store.StageUpdate, len(stages))
	for i, stage := range stages {
		updates[i] = store.StageUpdate{Stage: stage, Revision: 2}
	}
	data, err := json.Marshal(map[string]any{"version": 1, "id": "review", "seq": 1, "at": stages[0].StartedAt.Add(10 * time.Second), "update": map[string]any{"Operation": map[string]any{"revision": 2, "operation": "review", "started_at": stages[0].StartedAt, "finished_at": stages[0].StartedAt.Add(10 * time.Second), "status": "succeeded"}, "Stages": updates}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(strings.TrimSuffix(path, ".jsonl")+".timeline", append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

// +case=`Agent view isolates loop runs and model attempts, preserves concurrency, and never copies model bodies into its token summaries`
func TestStageAgentProjection(t *testing.T) {
	path, stages := stageAgentFixture(t)
	// Two parallel tool invocations have identical intervals; the inner invoke
	// shares the outer execution's identity and must not appear as a second tool.
	tool := stages[0]
	for _, stage := range stages {
		if stage.ID == "run-1/tool/same-call/1" {
			tool = stage
		}
	}
	invoke := tool
	invoke.ID = "invoke"
	invoke.ParentID = tool.ID
	invoke.Name = "tool.invoke"
	peer := tool
	peer.ID = "parallel-tool"
	peer.ParentID = "run-1/turn/1"
	peer.Attributes = nil
	stages = append(stages, invoke, peer)
	writeStageAgentFixture(t, path, stages)
	// Only the linked Session should be opened.
	if err := os.WriteFile(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", "unrelated.jsonl"), []byte("corrupt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := loadStageAgent(path, "execution-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Turns) != 1 || len(view.Turns[0].Items) != 4 || len(view.Operations) != 0 {
		t.Fatalf("wrong grouping: %+v", view)
	}
	if turn := view.Turns[0]; turn.OffsetMS != 0 || turn.DurationMS == nil || *turn.DurationMS != 10000 {
		t.Fatalf("wrong turn timing: %+v", turn)
	}
	calls := map[timeline.StageID]*agentItemView{}
	for _, item := range view.Turns[0].Items {
		calls[item.StageID] = item
	}
	failed, success := calls["run-1/model/model-1/1"], calls["run-1/model/model-1/2"]
	if failed == nil || failed.Usage != nil || failed.Error != "temporary provider failure" || success == nil || success.Usage == nil || success.Usage.Input != 100 || success.Attempt != 2 {
		t.Fatalf("attempt isolation: %+v", calls)
	}
	if calls[tool.ID].OffsetMS != calls[peer.ID].OffsetMS || *calls[tool.ID].DurationMS != *calls[peer.ID].DurationMS {
		t.Fatal("parallel intervals changed")
	}
	raw, _ := json.Marshal(view)
	for _, forbidden := range []string{"inspect caller", "read evidence", "check the guard", "run-2", "invoke"} {
		if bytes.Contains(raw, []byte(forbidden)) {
			t.Fatalf("unexpected %s in Agent projection", forbidden)
		}
	}
	if failed.Depth != 1 || success.Depth != 1 || calls[tool.ID].Depth != 2 || calls[peer.ID].Depth != 1 {
		t.Fatalf("Agent indentation lost recorded ancestry: failed=%d success=%d child=%d peer=%d", failed.Depth, success.Depth, calls[tool.ID].Depth, calls[peer.ID].Depth)
	}
	var parentIndex, childIndex int
	for i, item := range view.Turns[0].Items {
		if item.StageID == success.StageID {
			parentIndex = i
		}
		if item.StageID == tool.ID {
			childIndex = i
		}
	}
	if childIndex != parentIndex+1 {
		t.Fatal("Agent tree did not keep the child beneath its parent")
	}
	if calls[tool.ID].Label != "read_files" {
		t.Fatal("tool identity lost")
	}
}

func TestStageAgentRepeatedTurnIndexAndMissingUsage(t *testing.T) {
	path, stages := stageAgentFixture(t)
	var turn, model timeline.Stage
	for _, stage := range stages {
		if stage.ID == "run-2/turn/1" {
			turn = stage
		}
		if stage.ID == "run-2/model/model-1/1" {
			model = stage
		}
	}
	// A later wrap-up run can restart turn numbering within the same execution.
	turn.ParentID = "execution-1"
	for i := range stages {
		if stages[i].ID == turn.ID {
			stages[i] = turn
		}
	}
	model.ID = "missing-attempt"
	model.ParentID = turn.ID
	model.Attributes = map[string]json.RawMessage{"session_ref": json.RawMessage(`{"session_id":"session-1","run_id":"run-2","execution_id":"model-1","attempt":99}`)}
	model.FinishedAt = time.Time{}
	stages = append(stages, model)
	writeStageAgentFixture(t, path, stages)
	view, err := loadStageAgent(path, "execution-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Turns) != 2 || view.Turns[0].StageID == view.Turns[1].StageID {
		t.Fatalf("turns merged: %+v", view.Turns)
	}
	var missing *agentItemView
	for _, turn := range view.Turns {
		for _, item := range turn.Items {
			if item.StageID == model.ID {
				missing = item
			}
		}
	}
	if missing == nil || missing.Usage != nil || missing.DurationMS != nil || missing.Status != "incomplete" {
		t.Fatalf("missing evidence fabricated: %+v", missing)
	}
	if err := os.Remove(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", "loop.jsonl")); err != nil {
		t.Fatal(err)
	}
	view, err = loadStageAgent(path, "execution-1")
	if err != nil || len(view.Warnings) == 0 {
		t.Fatalf("missing Session: %+v %v", view, err)
	}
	for _, turn := range view.Turns {
		for _, item := range turn.Items {
			if item.Usage != nil {
				t.Fatal("invented usage")
			}
		}
	}
}

func TestStageAgentEndpointAndAvailableViews(t *testing.T) {
	path, stages := stageAgentFixture(t)
	root := filepath.Dir(filepath.Dir(path))
	repo := filepath.Base(filepath.Dir(path))
	response := httptest.NewRecorder()
	handleStageAgent(response, httptest.NewRequest("GET", "/agent?stage=execution-1", nil), root, repo, "review")
	if response.Code != 200 || response.Header().Get("Cache-Control") != "no-store" || !strings.Contains(response.Body.String(), `"turns"`) {
		t.Fatalf("response=%d %s", response.Code, response.Body.String())
	}
	for _, stage := range []string{"unknown", "run-1/model/model-1/1"} {
		response = httptest.NewRecorder()
		handleStageAgent(response, httptest.NewRequest("GET", "/agent?stage="+stage, nil), root, repo, "review")
		if response.Code != 404 {
			t.Fatalf("non-loop status=%d", response.Code)
		}
	}
	response = httptest.NewRecorder()
	handleStageAgent(response, httptest.NewRequest("GET", "/agent?stage=execution-1", nil), root, "../repo", "review")
	if response.Code != 400 {
		t.Fatal("accepted traversal")
	}
	tmpl, err := parseTemplate("session.html")
	if err != nil {
		t.Fatal(err)
	}
	var html bytes.Buffer
	if err := tmpl.Execute(&html, sessionPageData{Session: &ViewSession{Timeline: &timeline.Snapshot{Stages: stages}}}); err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		section := strings.Split(strings.Split(html.String(), `data-stage-detail="`+string(stage.ID)+`">`)[1], "</template>")[0]
		has := func(name string) bool { return strings.Contains(section, `data-stage-view="`+name+`"`) }
		if !has("default") || has("model") != (stage.Name == "model.attempt") || has("agent") != (stage.Name == "execution") {
			t.Fatalf("wrong views for %s: %s", stage.ID, section)
		}
	}
}

// +case=`A damaged linked Session keeps the Agent endpoint available, preserves valid usage, and never fabricates unread usage`
func TestStageAgentSessionFailuresPreserveTree(t *testing.T) {
	for _, tc := range []struct {
		name         string
		change       func([]byte) []byte
		unreadable   bool
		wantUsage    bool
		wantToolRead bool
	}{
		{name: "malformed envelope after valid records", change: func(data []byte) []byte { return append(data, []byte("{broken\n")...) }, wantUsage: true, wantToolRead: true},
		{name: "malformed envelope before calls", change: func(data []byte) []byte { return append([]byte("{broken\n"), data...) }},
		{name: "invalid event before successful retry", change: func(data []byte) []byte {
			return bytes.Replace(data, []byte(`{"error":"temporary provider failure"}`), []byte(`"invalid event"`), 1)
		}, wantUsage: true, wantToolRead: true},
		{name: "invalid model response before tool", change: func(data []byte) []byte {
			return bytes.Replace(data, []byte(`"$type":"agentgo.message.v1"`), []byte(`"$type":"unknown.message"`), 1)
		}, wantToolRead: true},
		{name: "linked file read failure", unreadable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _ := stageAgentFixture(t)
			file := filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", "loop.jsonl")
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if tc.unreadable {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(file, tc.change(data), 0600); err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handleStageAgent(response, httptest.NewRequest("GET", "/agent?stage=execution-1", nil), filepath.Dir(filepath.Dir(path)), filepath.Base(filepath.Dir(path)), "review")
			if response.Code != 200 {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var view agentView
			if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if len(view.Turns) != 1 || len(view.Turns[0].Items) != 3 || len(view.Warnings) == 0 {
				t.Fatalf("lost tree or diagnostics: %+v", view)
			}
			if strings.Contains(response.Body.String(), filepath.Dir(path)) || !strings.Contains(strings.Join(view.Warnings, " "), "loop.jsonl") {
				t.Fatalf("diagnostics must identify the Session without exposing local paths: %v", view.Warnings)
			}
			for _, item := range view.Turns[0].Items {
				if item.StageID == "run-1/model/model-1/2" {
					if (item.Usage != nil) != tc.wantUsage || (item.Usage != nil && item.Usage.Input != 100) {
						t.Fatalf("wrong retained usage: %+v", item)
					}
				}
				if item.Kind == "tool" && tc.wantToolRead && item.Label != "read_files" {
					t.Fatalf("did not read valid record after damaged event: %+v", item)
				}
			}
		})
	}
}

func TestStageAgentSourceErrorDoesNotExposePath(t *testing.T) {
	root := t.TempDir()
	response := httptest.NewRecorder()
	handleStageAgent(response, httptest.NewRequest("GET", "/agent?stage=execution-1", nil), root, "repo", "missing")
	if response.Code != 404 || strings.Contains(response.Body.String(), root) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

// A large review can contain many stages unrelated to the selected loop.
func BenchmarkAgentHierarchyManyTurns(b *testing.B) {
	source := &stageSource{byID: map[timeline.StageID]timeline.Stage{}}
	for i := 0; i < 20000; i++ {
		stage := timeline.Stage{ID: timeline.StageID(fmt.Sprintf("stage-%d", i))}
		source.rows = append(source.rows, stage)
		source.byID[stage.ID] = stage
	}
	stageOrder := map[timeline.StageID]int{}
	turns := make([][]*agentItemView, 100)
	for i := range turns {
		stageOrder[source.rows[i*2].ID] = i * 2
		stageOrder[source.rows[i*2+1].ID] = i*2 + 1
		turns[i] = []*agentItemView{{StageID: source.rows[i*2].ID}, {StageID: source.rows[i*2+1].ID}}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		for _, items := range turns {
			agentHierarchy(items, source, "loop", stageOrder)
		}
	}
}

func TestFailedModelPartialEvidenceInCallAndAgentViews(t *testing.T) {
	path, _ := stageAgentFixture(t)
	sessionPath := filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", "loop.jsonl")
	files, err := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + ".sessions/*.jsonl")
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%v err=%v", files, err)
	}
	sessionPath = files[0]
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	changed := false
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		payload, _ := record["data"].(map[string]any)
		if record["type"] == "model_exec_end" && payload["error"] == "temporary provider failure" {
			codec, err := msg.Codec()
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := codec.Marshal(agentgo.Message{Role: agentgo.RoleAssistant, Content: []agentgo.ContentBlock{agentgo.TextBlock("unfinished diagnostic")}, Usage: &agentgo.Usage{Input: 4, Output: 9, Reasoning: 7}})
			if err != nil {
				t.Fatal(err)
			}
			payload["partial_message"] = json.RawMessage(encoded)
			delete(payload, "message")
			changed = true
		}
		if err := json.NewEncoder(&output).Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if !changed {
		t.Fatal("failed fixture call missing")
	}
	if err := os.WriteFile(sessionPath, output.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	call, err := loadStageCall(path, "run-1/model/model-1/1")
	if err != nil {
		t.Fatal(err)
	}
	if call.Error == "" || call.Response == nil || call.Response.TextContent() != "unfinished diagnostic" || call.Response.Usage.Reasoning != 7 {
		t.Fatalf("call=%+v", call)
	}
	view, err := loadStageAgent(path, "execution-1")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, turn := range view.Turns {
		for _, item := range turn.Items {
			if item.StageID == "run-1/model/model-1/1" {
				found = true
				if item.Error == "" || item.Usage == nil || item.Usage.Output != 9 {
					t.Fatalf("item=%+v", item)
				}
			}
		}
	}
	if !found {
		t.Fatal("failed attempt missing")
	}
}
