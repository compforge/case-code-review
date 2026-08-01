package runner

import (
	"context"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/project"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestDossierCoordinatorUsesStrongRelationsAndOnlyWeightsLocality(t *testing.T) {
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
	var dossiers []hypothesisreview.Dossier
	coordinator := newDossierCoordinator(dossierCoordinatorConfig{
		Context: context.Background(), Units: units, Selections: selections,
		QuietWindow: time.Hour, MaxWait: time.Hour, MaxHypotheses: 6, Concurrency: 1,
		Review: func(_ context.Context, dossier hypothesisreview.Dossier) []hypothesisreview.Assessment {
			dossiers = append(dossiers, dossier)
			return nil
		},
	})
	coordinator.Submit(testHypothesis("h-1", "u-1", "component/a/x.go", "shared/evidence.go:1"))
	coordinator.Submit(testHypothesis("h-2", "u-2", "component/a/y.go", "shared/evidence.go:8"))
	coordinator.Submit(testHypothesis("h-3", "u-3", "component/b/z.go", "other/evidence.go:1"))
	coordinator.Finish()

	if len(dossiers) != 2 {
		t.Fatalf("dossiers = %+v, want related evidence merged and locality-only candidate separate", dossiers)
	}
	for _, dossier := range dossiers {
		if len(dossier.Hypotheses) == 2 {
			return
		}
	}
	t.Fatalf("no two-hypothesis dossier found: %+v", dossiers)
}

func TestDossierCoordinatorHighReadOverlapCanFormOneDossier(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "a.go", "a.go::A"),
		testReviewUnit("u-2", "b.go", "b.go::B"),
	}
	reads := map[string][]string{
		"u-1": {"shared/x.go", "shared/y.go", "left.go"},
		"u-2": {"shared/x.go", "shared/y.go", "right.go"},
	}
	var dossiers []hypothesisreview.Dossier
	coordinator := newDossierCoordinator(dossierCoordinatorConfig{
		Context: context.Background(), Units: units,
		QuietWindow: time.Hour, MaxWait: time.Hour, MaxHypotheses: 6,
		ReadPaths: func(id string) []string { return reads[id] },
		Review: func(_ context.Context, dossier hypothesisreview.Dossier) []hypothesisreview.Assessment {
			dossiers = append(dossiers, dossier)
			return nil
		},
	})
	coordinator.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	coordinator.Submit(testHypothesis("h-2", "u-2", "b.go", "b.go:1"))
	coordinator.Finish()
	if len(dossiers) != 1 || len(dossiers[0].Hypotheses) != 2 {
		t.Fatalf("high read overlap did not merge: %+v", dossiers)
	}
	if !slices.Contains(dossiers[0].EvidencePaths, "shared/x.go") || !slices.Contains(dossiers[0].EvidencePaths, "shared/y.go") {
		t.Fatalf("Dossier did not retain the Unit Review read footprint: %v", dossiers[0].EvidencePaths)
	}
}

func TestDossierCoordinatorRelatesAUnitThatReadThePeersTarget(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "a.go", "a.go::A"),
		testReviewUnit("u-2", "b.go", "b.go::B"),
	}
	reads := map[string][]string{"u-1": {"b.go"}}
	var dossiers []hypothesisreview.Dossier
	coordinator := newDossierCoordinator(dossierCoordinatorConfig{
		Context: context.Background(), Units: units,
		QuietWindow: time.Hour, MaxWait: time.Hour, MaxHypotheses: 6,
		ReadPaths: func(id string) []string { return reads[id] },
		Review: func(_ context.Context, dossier hypothesisreview.Dossier) []hypothesisreview.Assessment {
			dossiers = append(dossiers, dossier)
			return nil
		},
	})
	coordinator.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	coordinator.Submit(testHypothesis("h-2", "u-2", "b.go", "b.go:1"))
	coordinator.Finish()
	if len(dossiers) != 1 || len(dossiers[0].Hypotheses) != 2 {
		t.Fatalf("peer target read did not relate Dossiers: %+v", dossiers)
	}
}

func TestLateRelatedDossierWaitsForPriorAssessment(t *testing.T) {
	units := []unit.Unit{
		testReviewUnit("u-1", "a.go", "a.go::A"),
		testReviewUnit("u-2", "a.go", "a.go::B"),
	}
	firstStarted := make(chan hypothesisreview.Dossier, 1)
	secondStarted := make(chan hypothesisreview.Dossier, 1)
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	coordinator := newDossierCoordinator(dossierCoordinatorConfig{
		Context: context.Background(), Units: units,
		QuietWindow: 20 * time.Millisecond, MaxWait: time.Second,
		MaxHypotheses: 6, Concurrency: 2,
		Review: func(_ context.Context, dossier hypothesisreview.Dossier) []hypothesisreview.Assessment {
			mu.Lock()
			calls++
			call := calls
			mu.Unlock()
			if call == 1 {
				firstStarted <- dossier
				<-releaseFirst
				return []hypothesisreview.Assessment{{HypothesisID: dossier.Hypotheses[0].ID, DossierID: dossier.ID}}
			}
			secondStarted <- dossier
			return nil
		},
	})
	coordinator.Submit(testHypothesis("h-1", "u-1", "a.go", "a.go:1"))
	first := <-firstStarted
	coordinator.Submit(testHypothesis("h-2", "u-2", "a.go", "a.go:2"))

	finished := make(chan struct{})
	go func() {
		coordinator.Finish()
		close(finished)
	}()
	select {
	case dossier := <-secondStarted:
		t.Fatalf("late dossier started before prior completed: %+v", dossier)
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	second := <-secondStarted
	<-finished
	if len(second.PriorDossierIDs) != 1 || second.PriorDossierIDs[0] != first.ID {
		t.Fatalf("prior dossier IDs = %v, want %s", second.PriorDossierIDs, first.ID)
	}
	if len(second.PriorAssessments) != 1 || second.PriorAssessments[0].HypothesisID != "h-1" {
		t.Fatalf("prior assessments = %+v", second.PriorAssessments)
	}
}

func TestDossierCoordinatorCapsMembershipAndReviewConcurrency(t *testing.T) {
	var units []unit.Unit
	for i := 0; i < 7; i++ {
		units = append(units, testReviewUnit("same-"+string(rune('a'+i)), "same.go", "same.go::S"))
	}
	var sizesMu sync.Mutex
	var sizes []int
	coordinator := newDossierCoordinator(dossierCoordinatorConfig{
		Context: context.Background(), Units: units,
		QuietWindow: time.Hour, MaxWait: time.Hour, MaxHypotheses: 6, Concurrency: 2,
		Review: func(_ context.Context, dossier hypothesisreview.Dossier) []hypothesisreview.Assessment {
			sizesMu.Lock()
			sizes = append(sizes, len(dossier.Hypotheses))
			sizesMu.Unlock()
			return nil
		},
	})
	for i, reviewUnit := range units {
		coordinator.Submit(testHypothesis("cap-"+string(rune('a'+i)), reviewUnit.ID, "same.go", "same.go:1"))
	}
	coordinator.Finish()
	sort.Ints(sizes)
	if len(sizes) != 2 || sizes[0] != 1 || sizes[1] != 6 {
		t.Fatalf("dossier sizes = %v, want [1 6]", sizes)
	}

	var independent []unit.Unit
	for i := 0; i < 3; i++ {
		independent = append(independent, testReviewUnit("u-"+string(rune('a'+i)), string(rune('a'+i))+".go", string(rune('a'+i))+".go::F"))
	}
	started := make(chan struct{}, 3)
	release := make(chan struct{})
	var active, maximum atomic.Int64
	limited := newDossierCoordinator(dossierCoordinatorConfig{
		Context: context.Background(), Units: independent,
		QuietWindow: time.Hour, MaxWait: time.Hour, MaxHypotheses: 6, Concurrency: 2,
		Review: func(context.Context, hypothesisreview.Dossier) []hypothesisreview.Assessment {
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
			return nil
		},
	})
	for i, reviewUnit := range independent {
		limited.Submit(testHypothesis("independent-"+string(rune('a'+i)), reviewUnit.ID, reviewUnit.Path(), reviewUnit.Path()+":1"))
	}
	finished := make(chan struct{})
	go func() { limited.Finish(); close(finished) }()
	<-started
	<-started
	select {
	case <-started:
		t.Fatal("third Review 2 started above the concurrency cap")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-finished
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", maximum.Load())
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
