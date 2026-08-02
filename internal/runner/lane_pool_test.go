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
		Review: func(_ context.Context, dossier hypothesisreview.Dossier, _ *harness.ExecutionResult) hypothesisreview.ReviewResult {
			mu.Lock()
			lanes[dossier.Hypotheses[0].ID] = dossier.LaneID
			mu.Unlock()
			return hypothesisreview.ReviewResult{}
		},
	})
	pool.Submit(testHypothesis("h-1", "u-1", "component/a/x.go", "shared/evidence.go:1"))
	pool.Submit(testHypothesis("h-2", "u-2", "component/a/y.go", "shared/evidence.go:8"))
	pool.Submit(testHypothesis("h-3", "u-3", "component/b/z.go", "other/evidence.go:1"))
	pool.Finish()

	if len(lanes) != 3 {
		t.Fatalf("reviewed Dossiers = %v, want one per Hypothesis", lanes)
	}
	if lanes["h-1"] == "" || lanes["h-1"] != lanes["h-2"] {
		t.Fatalf("related evidence was not assigned to one Lane: %v", lanes)
	}
	if lanes["h-3"] == lanes["h-1"] {
		t.Fatalf("locality alone merged unrelated Dossiers: %v", lanes)
	}
}

func TestLanePoolUsesUnitReviewReadOverlap(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "a.go", "a.go::A"),
		testReviewUnit("u-2", "b.go", "b.go::B"),
	}
	reads := map[string][]string{
		"u-1": {"shared/x.go", "shared/y.go", "left.go"},
		"u-2": {"shared/x.go", "shared/y.go", "right.go"},
	}
	var dossiers []hypothesisreview.Dossier
	var mu sync.Mutex
	pool := newLanePool(lanePoolConfig{
		Context: context.Background(), Units: units,
		ReadPaths: func(id string) []string { return reads[id] },
		Review: func(_ context.Context, dossier hypothesisreview.Dossier, _ *harness.ExecutionResult) hypothesisreview.ReviewResult {
			mu.Lock()
			dossiers = append(dossiers, dossier)
			mu.Unlock()
			return hypothesisreview.ReviewResult{}
		},
	})
	pool.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	pool.Submit(testHypothesis("h-2", "u-2", "b.go", "b.go:1"))
	pool.Finish()
	if len(dossiers) != 2 || dossiers[0].LaneID != dossiers[1].LaneID {
		t.Fatalf("high read overlap did not share a Lane: %+v", dossiers)
	}
	for _, dossier := range dossiers {
		if !slices.Contains(dossier.EvidencePaths, "shared/x.go") || !slices.Contains(dossier.EvidencePaths, "shared/y.go") {
			t.Fatalf("Dossier did not retain its Unit Review read footprint: %v", dossier.EvidencePaths)
		}
	}
}

func TestLaneSerializesRelatedDossiersAndContinuesContext(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "a.go", "a.go::A"),
		testReviewUnit("u-2", "a.go", "a.go::B"),
	}
	firstStarted := make(chan hypothesisreview.Dossier, 1)
	secondStarted := make(chan hypothesisreview.Dossier, 1)
	releaseFirst := make(chan struct{})
	var calls atomic.Int64
	pool := newLanePool(lanePoolConfig{
		Context: context.Background(), Units: units, Concurrency: 2,
		Review: func(_ context.Context, dossier hypothesisreview.Dossier, continuation *harness.ExecutionResult) hypothesisreview.ReviewResult {
			if calls.Add(1) == 1 {
				if continuation != nil {
					t.Error("first Dossier unexpectedly received continuation")
				}
				firstStarted <- dossier
				<-releaseFirst
				return hypothesisreview.ReviewResult{
					Assessments:      []hypothesisreview.Assessment{{HypothesisID: dossier.Hypotheses[0].ID, DossierID: dossier.ID}},
					EvidenceReceipts: []hypothesisreview.EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
					Execution:        harness.ExecutionResult{State: harness.OutcomeCompleted},
				}
			}
			if continuation == nil {
				t.Error("second related Dossier did not continue the Lane context")
			}
			secondStarted <- dossier
			return hypothesisreview.ReviewResult{}
		},
	})
	pool.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	first := <-firstStarted
	pool.Submit(testHypothesis("h-2", "u-2", "a.go", "a.go:2"))

	finished := make(chan struct{})
	go func() { pool.Finish(); close(finished) }()
	select {
	case dossier := <-secondStarted:
		t.Fatalf("related Dossier started before its Lane was idle: %+v", dossier)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	second := <-secondStarted
	<-finished
	if second.LaneID != first.LaneID {
		t.Fatalf("related Dossiers changed Lane: %s != %s", second.LaneID, first.LaneID)
	}
	if len(second.PriorDossierIDs) != 1 || second.PriorDossierIDs[0] != first.ID {
		t.Fatalf("prior dossier IDs = %v, want %s", second.PriorDossierIDs, first.ID)
	}
	if len(second.PriorAssessments) != 1 || second.PriorAssessments[0].HypothesisID != "h-1" {
		t.Fatalf("prior assessments = %+v", second.PriorAssessments)
	}
	if len(second.PriorEvidence) != 1 || second.PriorEvidence[0].Ref != "a.go" {
		t.Fatalf("prior evidence = %+v", second.PriorEvidence)
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
		Review: func(context.Context, hypothesisreview.Dossier, *harness.ExecutionResult) hypothesisreview.ReviewResult {
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
