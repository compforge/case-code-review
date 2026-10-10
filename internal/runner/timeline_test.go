package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner/formation"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func TestPipelineTimelineCoversFormationReviewAndTrial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	history := session.New(t.TempDir(), "main", "test", session.SessionOptions{})
	ctx := history.Context(context.Background())
	a := New(Args{RepoDir: history.RepoDir, Session: history, LLMClient: &budgetClient{}, MaxConcurrency: 1,
		Template: template.Template{MainTask: template.LlmConversation{Messages: []template.ChatMessage{{Role: "user", Content: "review {{diff}}"}}}, MaxTokens: 10000, MaxToolRequestTimes: 2},
	})
	a.changes = []change.Change{goDiff("p.go", 1)}
	if _, err := a.dispatchUnits(ctx); err != nil {
		t.Fatal(err)
	}
	history.Finalize()
	recorder, _ := timeline.FromContext(ctx)
	snapshot, err := recorder.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, stage := range snapshot.Stages {
		names[stage.Name] = true
		if stage.Status == timeline.Running {
			t.Errorf("stage not finished: %+v", stage)
		}
	}
	stages := map[timeline.StageID]string{}
	for _, stage := range snapshot.Stages {
		stages[stage.ID] = stage.Name
	}
	for _, stage := range snapshot.Stages {
		if stage.Name == "unit.grouping" && stages[stage.ParentID] != "unit.formation" {
			t.Fatalf("grouping has wrong parent: %+v", stage)
		}
	}

	for _, name := range []string{"unit.formation", "unit.grouping", "codegraph.build", "unit.queue", "review.unit", "execution", "turn", "context.project", "model.attempt", "llm.request", "trial.finalize"} {
		if !names[name] {
			t.Errorf("missing %s in %v", name, names)
		}
	}
	paths, err := filepath.Glob(filepath.Join(home, ".casecodereview", "*", "*", history.SessionID+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("session files=%v err=%v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	foundStep, foundReport := false, false
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record struct {
			Kind    string                     `json:"artifact_kind"`
			StageID timeline.StageID           `json:"stage_id"`
			Data    map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		switch record.Kind {
		case "unit_grouping":
			var step struct {
				Strategy string `json:"strategy"`
				Input    int    `json:"input_units"`
				Output   int    `json:"output_units"`
			}
			if err := json.Unmarshal(record.Data["step"], &step); err != nil {
				t.Fatal(err)
			}
			if (step.Strategy != "local" && step.Strategy != "relations" && step.Strategy != "file") || step.Input != 1 || step.Output != 1 || stages[record.StageID] != "unit.grouping" {
				t.Fatalf("step=%+v stage=%s", step, record.StageID)
			}
			foundStep = foundStep || step.Strategy == "local"
		case "unit_formation":
			var report struct {
				Initial int `json:"initial_units"`
				Final   int `json:"final_units"`
				Max     int `json:"max_units"`
			}
			if err := json.Unmarshal(record.Data["grouping"], &report); err != nil {
				t.Fatal(err)
			}
			if report.Initial != 1 || report.Final != 1 || report.Max != 1 {
				t.Fatalf("report=%+v", report)
			}
			foundReport = true
		}
	}
	if !foundStep || !foundReport {
		t.Fatalf("missing persisted grouping: step=%v report=%v", foundStep, foundReport)
	}

}

func TestRunFinalizesTimelineOnDiffFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	a := New(Args{RepoDir: t.TempDir()}) // no Git repository: failure before dispatch
	if _, err := a.Run(context.Background()); err == nil {
		t.Fatal("expected diff load failure")
	}
	recorder, _ := timeline.FromContext(a.session.Context(context.Background()))
	snapshot, err := recorder.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != timeline.Failed || snapshot.FinishedAt.IsZero() {
		t.Fatalf("run=%+v", snapshot)
	}
	if len(snapshot.Stages) != 2 || snapshot.Stages[0].Name != "diff.load" || snapshot.Stages[0].Status != timeline.Failed || snapshot.Stages[1].Name != "diff.capture" || snapshot.Stages[1].Status != timeline.Failed || snapshot.Stages[1].ParentID != snapshot.Stages[0].ID {
		t.Fatalf("stages=%+v", snapshot.Stages)
	}
}

func TestLaneTimelineCancelsWaitingWorkWithoutReview(t *testing.T) {
	recorder, _ := timeline.New("lanes")
	if err := recorder.Start(context.Background(), "review"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(timeline.NewContext(context.Background(), recorder))
	defer cancel()
	units := []unit.Unit{testReviewUnit("u1", "a.go", "a.go::A"), testReviewUnit("u2", "b.go", "b.go::B")}
	started := make(chan struct{}, 2)
	assigned := make(chan struct{}, 2)
	pool := newLanePool(lanePoolConfig{Context: ctx, Units: units, Concurrency: 1,
		OnAssigned: func(hypothesisreview.ReviewInput, string) { assigned <- struct{}{} },
		Review: func(ctx context.Context, _ hypothesisreview.ReviewInput, _ *harness.ExecutionResult) hypothesisreview.ReviewResult {
			started <- struct{}{}
			<-ctx.Done()
			return hypothesisreview.ReviewResult{Execution: harness.ExecutionResult{State: harness.OutcomeAborted}}
		}})
	pool.Submit(testHypothesis("h1", "u1", "a.go", "a.go:1"))
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first review did not start")
	}
	pool.Submit(testHypothesis("h2", "u2", "b.go", "b.go:1"))
	for i := 0; i < 2; i++ {
		select {
		case <-assigned:
		case <-time.After(5 * time.Second):
			t.Fatal("lane assignment stalled")
		}
	}
	cancel()
	finished := make(chan struct{})
	go func() { pool.Finish(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled pool did not settle")
	}
	if len(started) != 0 {
		t.Fatal("waiting review started after cancellation")
	}
	snapshot, err := recorder.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	queues, reviews := 0, 0
	for _, stage := range snapshot.Stages {
		if stage.Status == timeline.Running {
			t.Fatalf("pending stage after cancellation: %+v", stage)
		}
		if stage.Name == "lane.queue" {
			queues++
		}
		if stage.Name == "review.hypothesis" {
			reviews++
			if stage.Status != timeline.Canceled {
				t.Errorf("review status=%s", stage.Status)
			}
		}
	}
	if queues != 2 || reviews != 1 {
		t.Fatalf("queues=%d reviews=%d", queues, reviews)
	}
}

func TestGroupingUsesConfiguredCeilingAndPersistsStage(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	src := "package p\nfunc A(){}\n"
	for _, path := range []string{"a.go", "b.go"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	history := session.New(repo, "main", "test", session.SessionOptions{})
	a := New(Args{RepoDir: repo, Session: history, MaxUnits: 1})
	a.changes = []change.Change{goDiff("a.go", 1), goDiff("b.go", 1)}
	if _, err := a.splitUnits(history.Context(t.Context())); err != nil {
		t.Fatal(err)
	}
	history.Finalize()
	paths, err := filepath.Glob(filepath.Join(home, ".casecodereview", "*", "*", history.SessionID+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record struct {
			Kind    string `json:"artifact_kind"`
			StageID string `json:"stage_id"`
			Data    struct {
				Step formation.GroupingStep `json:"step"`
			} `json:"data"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if record.Kind != "unit_grouping" || record.Data.Step.Strategy != "local" {
			continue
		}
		step := record.Data.Step
		if record.StageID == "" || step.OutputUnits != 2 || a.grouping.MaxUnits != 1 || a.grouping.FinalUnits != 1 {
			t.Fatalf("step=%+v stage=%s grouping=%+v", step, record.StageID, a.grouping)
		}
		for _, grouped := range a.grouping.Steps {
			if grouped.Strategy == "namespace" && grouped.OutputUnits == 1 && len(grouped.Merges) == 1 {
				return
			}
		}
		t.Fatal("namespace merge to configured ceiling missing")
	}
	t.Fatal("local grouping artifact missing")
}
