package runner

import (
	"context"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func TestPipelineTimelineCoversFormationReviewAndTrial(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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
	for _, name := range []string{"unit.formation", "codegraph.build", "unit.queue", "review.unit", "execution", "turn", "context.project", "model.attempt", "llm.request", "trial.finalize"} {
		if !names[name] {
			t.Errorf("missing %s in %v", name, names)
		}
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
	if len(snapshot.Stages) != 1 || snapshot.Stages[0].Name != "diff.load" || snapshot.Stages[0].Status != timeline.Failed {
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
