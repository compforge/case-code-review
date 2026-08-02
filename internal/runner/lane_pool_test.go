package runner

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/project"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestLanePoolUsesStrongRelationsAndOnlyWeightsLocality(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "component/a/x.go", "component/a/x.go::X"),
		testReviewUnit("u-2", "component/a/y.go", "component/a/y.go::Y"),
		testReviewUnit("u-3", "component/b/z.go", "component/b/z.go::Z"),
	}
	selections := map[string]fileSelection{}
	for _, reviewUnit := range units {
		selections[reviewUnit.Path()] = fileSelection{
			HasComponent: true, Component: project.Component{Root: "component", Kind: project.Go},
		}
	}
	lanes := make(map[string]string)
	var mu sync.Mutex
	pool := newLanePool(lanePoolConfig{
		Context: context.Background(), Units: units, Selections: selections,
		Review: func(_ context.Context, input hypothesisreview.ReviewInput, _ *harness.ExecutionResult) hypothesisreview.ReviewResult {
			mu.Lock()
			lanes[input.Hypothesis.ID] = input.LaneID
			mu.Unlock()
			return hypothesisreview.ReviewResult{}
		},
	})
	pool.Submit(testHypothesis("h-1", "u-1", "component/a/x.go", "shared/evidence.go:1"))
	pool.Submit(testHypothesis("h-2", "u-2", "component/a/y.go", "shared/evidence.go:8"))
	pool.Submit(testHypothesis("h-3", "u-3", "component/b/z.go", "other/evidence.go:1"))
	pool.Finish()

	if len(lanes) != 3 {
		t.Fatalf("reviewed Hypotheses = %v, want all inputs", lanes)
	}
	if lanes["h-1"] == "" || lanes["h-1"] != lanes["h-2"] {
		t.Fatalf("related evidence was not assigned to one Lane: %v", lanes)
	}
	if lanes["h-3"] == lanes["h-1"] {
		t.Fatalf("locality alone merged unrelated Hypotheses: %v", lanes)
	}
}

func TestLanePoolUsesUnitReviewReadOverlap(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "a.go", "a.go::A"),
		testReviewUnit("u-2", "b.go", "b.go::B"),
	}
	for i := range units {
		units[i].InitReviewState()
	}
	for _, path := range []string{"shared/x.go", "shared/y.go", "left.go"} {
		units[0].AddFileSnapshot(unit.FileSnapshot{Kind: unit.CurrentSnapshot, Path: path, Content: path})
	}
	for _, path := range []string{"shared/x.go", "shared/y.go", "right.go"} {
		units[1].AddFileSnapshot(unit.FileSnapshot{Kind: unit.CurrentSnapshot, Path: path, Content: path})
	}
	var inputs []hypothesisreview.ReviewInput
	var mu sync.Mutex
	pool := newLanePool(lanePoolConfig{
		Context: context.Background(), Units: units,
		Review: func(_ context.Context, input hypothesisreview.ReviewInput, _ *harness.ExecutionResult) hypothesisreview.ReviewResult {
			mu.Lock()
			inputs = append(inputs, input)
			mu.Unlock()
			return hypothesisreview.ReviewResult{}
		},
	})
	pool.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	pool.Submit(testHypothesis("h-2", "u-2", "b.go", "b.go:1"))
	pool.Finish()
	if len(inputs) != 2 || inputs[0].LaneID != inputs[1].LaneID {
		t.Fatalf("high read overlap did not share a Lane: %+v", inputs)
	}
	for _, input := range inputs {
		if !slices.Contains(input.Paths(), "shared/x.go") || !slices.Contains(input.Paths(), "shared/y.go") {
			t.Fatalf("Review input did not retain its Unit snapshots: %v", input.Paths())
		}
	}
}

func TestLaneSerializesRelatedHypothesesAndContinuesContext(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "a.go", "a.go::A"),
		testReviewUnit("u-2", "a.go", "a.go::B"),
	}
	for i := range units {
		units[i].InitReviewState()
		units[i].AddFileSnapshot(unit.FileSnapshot{
			Kind: unit.CurrentSnapshot, Path: "shared.go", Content: "File: shared.go (Total lines: 1)\n1|shared",
		})
	}
	firstStarted := make(chan hypothesisreview.ReviewInput, 1)
	secondStarted := make(chan hypothesisreview.ReviewInput, 1)
	releaseFirst := make(chan struct{})
	var calls atomic.Int64
	pool := newLanePool(lanePoolConfig{
		Context: context.Background(), Units: units, Concurrency: 2,
		Review: func(_ context.Context, input hypothesisreview.ReviewInput, continuation *harness.ExecutionResult) hypothesisreview.ReviewResult {
			if calls.Add(1) == 1 {
				if continuation != nil {
					t.Error("first Hypothesis unexpectedly received continuation")
				}
				firstStarted <- input
				<-releaseFirst
				return hypothesisreview.ReviewResult{
					Assessments:      []hypothesisreview.Assessment{{HypothesisID: input.Hypothesis.ID, LaneID: input.LaneID}},
					EvidenceReceipts: []hypothesisreview.EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
					Execution:        harness.ExecutionResult{State: harness.OutcomeCompleted},
				}
			}
			if continuation == nil {
				t.Error("second related Hypothesis did not continue the Lane context")
			}
			secondStarted <- input
			return hypothesisreview.ReviewResult{}
		},
	})
	pool.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	first := <-firstStarted
	pool.Submit(testHypothesis("h-2", "u-2", "a.go", "a.go:2"))

	finished := make(chan struct{})
	go func() { pool.Finish(); close(finished) }()
	select {
	case input := <-secondStarted:
		t.Fatalf("related Hypothesis started before its Lane was idle: %+v", input)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	second := <-secondStarted
	<-finished
	if second.LaneID != first.LaneID {
		t.Fatalf("related Hypotheses changed Lane: %s != %s", second.LaneID, first.LaneID)
	}
	if len(first.FileSnapshots) != 1 || len(second.FileSnapshots) != 0 {
		t.Fatalf("Lane snapshot delta first=%+v second=%+v, want shared file injected once", first.FileSnapshots, second.FileSnapshots)
	}
	if len(second.PriorAssessments) != 1 || second.PriorAssessments[0].HypothesisID != "h-1" {
		t.Fatalf("prior assessments = %+v", second.PriorAssessments)
	}
	if got := units[0].Review().Assessments; len(got) != 1 || got[0].HypothesisID != "h-1" {
		t.Fatalf("Assessment was not attached to origin Unit: %+v", got)
	}
	if len(second.PriorEvidence) != 1 || second.PriorEvidence[0].Ref != "a.go" {
		t.Fatalf("prior evidence = %+v", second.PriorEvidence)
	}
}

func TestLaneReinjectsContextWhenNoContinuationWasCreated(t *testing.T) {
	reviewUnit := testReviewUnit("u-1", "a.go", "a.go::A")
	reviewUnit.InitReviewState()
	reviewUnit.AddFileSnapshot(unit.FileSnapshot{Kind: unit.CurrentSnapshot, Path: "shared.go", Content: "shared"})

	var snapshotsPerCall []int
	pool := newLanePool(lanePoolConfig{
		Context: context.Background(), Units: []unit.Unit{reviewUnit},
		Review: func(_ context.Context, input hypothesisreview.ReviewInput, _ *harness.ExecutionResult) hypothesisreview.ReviewResult {
			snapshotsPerCall = append(snapshotsPerCall, len(input.FileSnapshots))
			return hypothesisreview.ReviewResult{} // setup failed: no retained Execution
		},
	})
	pool.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	pool.Submit(testHypothesis("h-2", "u-1", "a.go", "a.go:2"))
	pool.Finish()

	if len(snapshotsPerCall) != 2 || snapshotsPerCall[0] != 1 || snapshotsPerCall[1] != 1 {
		t.Fatalf("context snapshots per call = %v, want reinjection after setup failure", snapshotsPerCall)
	}
}

func TestLanePoolRunsIndependentLanesConcurrently(t *testing.T) {
	var units []unit.Unit
	for i := 0; i < 3; i++ {
		name := string(rune('a' + i))
		units = append(units, testReviewUnit("u-"+name, name+".go", name+".go::F"))
	}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var active, maximum atomic.Int64
	pool := newLanePool(lanePoolConfig{
		Context: context.Background(), Units: units, Concurrency: 2,
		Review: func(context.Context, hypothesisreview.ReviewInput, *harness.ExecutionResult) hypothesisreview.ReviewResult {
			now := active.Add(1)
			for {
				old := maximum.Load()
				if now <= old || maximum.CompareAndSwap(old, now) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return hypothesisreview.ReviewResult{}
		},
	})
	for i, reviewUnit := range units {
		pool.Submit(testHypothesis("h-"+string(rune('a'+i)), reviewUnit.ID, reviewUnit.Path(), reviewUnit.Path()+":1"))
	}
	finished := make(chan struct{})
	go func() { pool.Finish(); close(finished) }()
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("third Review 2 started above the Lane concurrency cap")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-finished
	if maximum.Load() != 2 {
		t.Fatalf("maximum Lane concurrency = %d, want 2", maximum.Load())
	}
}

func testReviewUnit(id, file, symbol string) unit.Unit {
	return unit.Unit{ID: id, Scope: unit.ScopeFunc, Fragments: []unit.Fragment{{Path: file, Symbols: []string{symbol}}}}
}

func testHypothesis(id, origin, file, evidence string) unitreview.Hypothesis {
	return unitreview.Hypothesis{
		ID: id, OriginUnit: origin, Path: file, Content: "issue", ExistingCode: "x",
		Trigger: "call", Impact: "failure", ChangeAttribution: "changed", Evidence: []string{evidence},
	}
}
