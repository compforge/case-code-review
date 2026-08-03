package formation

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type staticFinder []unit.Clue

func (f staticFinder) Find(unit.Unit) []unit.Clue { return f }

func TestFormDeduplicatesCluesAfterScopeIsFinal(t *testing.T) {
	clues := staticFinder{
		{Kind: unit.ClueSpec, Relation: unit.RelSelf, Text: "contract"},
		{Kind: unit.ClueSpec, Relation: unit.RelSelf, Text: "contract"},
		{Kind: unit.ClueSpec, Relation: unit.RelOwner, Text: "contract"},
		{Kind: unit.ClueRule, Relation: unit.RelUsed, Text: "per-request"},
	}
	units, _, err := Form(Config{
		Changes:  []change.Change{{NewPath: "a.go", Diff: "@@ -1 +1 @@\n-old\n+new"}},
		Splitter: unit.FileSplitter{},
		Finders:  []unit.ClueFinder{clues},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || len(units[0].Clues) != 3 {
		t.Fatalf("unexpected formed units: %+v", units)
	}
}

func TestMergeCallChainsFallsBackWhenResidualsExceedFileCount(t *testing.T) {
	a1 := unit.Fragment{Path: "a.ts", Symbols: []string{"a.ts::a1"}, Diff: "a1"}
	a2 := unit.Fragment{Path: "a.ts", Symbols: []string{"a.ts::a2"}, Diff: "a2"}
	aResidual := unit.Fragment{Path: "a.ts", Diff: "imports"}
	b1 := unit.Fragment{Path: "b.ts", Symbols: []string{"b.ts::b1"}, Diff: "b1"}
	b2 := unit.Fragment{Path: "b.ts", Symbols: []string{"b.ts::b2"}, Diff: "b2"}
	files := []unit.FileFragments{
		{Diff: change.Change{NewPath: "a.ts", Diff: "a1\na2\nimports"}, Fragments: []unit.Fragment{a1, a2, aResidual}},
		{Diff: change.Change{NewPath: "b.ts", Diff: "b1\nb2"}, Fragments: []unit.Fragment{b1, b2}},
	}

	units := mergeCallChains(files, map[string][]string{
		"a.ts::a1": {"b.ts::b1"},
		"b.ts::b1": {"a.ts::a1"},
	}, unit.WatermarkMerger{Watermark: DefaultWatermark})
	if len(units) != 2 {
		t.Fatalf("want one Unit per file instead of three chain/residual loops, got %d: %+v", len(units), units)
	}
	for i, reviewUnit := range units {
		if reviewUnit.Scope != unit.ScopeFile || reviewUnit.Formed != unit.FormedCoalesce {
			t.Fatalf("Unit %d did not fall back to file scope: %+v", i, reviewUnit)
		}
	}
	if got := units[0].Diff(); !strings.Contains(got, "a1") || !strings.Contains(got, "a2") || !strings.Contains(got, "imports") {
		t.Fatalf("a.ts file Unit lost content: %q", got)
	}
	if got := units[1].Diff(); !strings.Contains(got, "b1") || !strings.Contains(got, "b2") {
		t.Fatalf("b.ts file Unit lost content: %q", got)
	}
}

func TestMergeCallChainsKeepsGroupingWhenItDoesNotExpandLoops(t *testing.T) {
	a1 := unit.Fragment{Path: "a.ts", Symbols: []string{"a.ts::a1"}, Diff: "a1"}
	b1 := unit.Fragment{Path: "b.ts", Symbols: []string{"b.ts::b1"}, Diff: "b1"}
	c1 := unit.Fragment{Path: "c.ts", Symbols: []string{"c.ts::c1"}, Diff: "c1"}
	c2 := unit.Fragment{Path: "c.ts", Symbols: []string{"c.ts::c2"}, Diff: "c2"}
	c3 := unit.Fragment{Path: "c.ts", Symbols: []string{"c.ts::c3"}, Diff: "c3"}
	files := []unit.FileFragments{
		{Diff: change.Change{NewPath: "a.ts", Diff: "a1"}, Fragments: []unit.Fragment{a1}},
		{Diff: change.Change{NewPath: "b.ts", Diff: "b1"}, Fragments: []unit.Fragment{b1}},
		{Diff: change.Change{NewPath: "c.ts", Diff: "c1\nc2\nc3"}, Fragments: []unit.Fragment{c1, c2, c3}},
	}

	units := mergeCallChains(files, map[string][]string{
		"a.ts::a1": {"b.ts::b1"},
		"b.ts::b1": {"a.ts::a1"},
	}, unit.WatermarkMerger{Watermark: DefaultWatermark})
	if len(units) != 2 {
		t.Fatalf("want one chain and one independent Unit, got %d: %+v", len(units), units)
	}
	if units[0].Scope != unit.ScopeCallChain {
		t.Fatalf("first Unit = %s, want callchain", units[0].Scope)
	}
	if units[1].Scope != unit.ScopeFile || units[1].ID != "c.ts" {
		t.Fatalf("busy independent file did not coalesce: %+v", units[1])
	}
}

func TestMergeCallChainsDropsOnlyExpandingChain(t *testing.T) {
	a1 := unit.Fragment{Path: "a.ts", Symbols: []string{"a.ts::a1"}, Diff: "a1"}
	a2 := unit.Fragment{Path: "a.ts", Symbols: []string{"a.ts::a2"}, Diff: "a2"}
	b1 := unit.Fragment{Path: "b.ts", Symbols: []string{"b.ts::b1"}, Diff: "b1"}
	b2 := unit.Fragment{Path: "b.ts", Symbols: []string{"b.ts::b2"}, Diff: "b2"}
	c1 := unit.Fragment{Path: "c.ts", Symbols: []string{"c.ts::c1"}, Diff: "c1"}
	c2 := unit.Fragment{Path: "c.ts", Symbols: []string{"c.ts::c2"}, Diff: "c2"}
	d1 := unit.Fragment{Path: "d.ts", Symbols: []string{"d.ts::d1"}, Diff: "d1"}
	files := []unit.FileFragments{
		{Diff: change.Change{NewPath: "a.ts", Diff: "a1\na2"}, Fragments: []unit.Fragment{a1, a2}},
		{Diff: change.Change{NewPath: "b.ts", Diff: "b1\nb2"}, Fragments: []unit.Fragment{b1, b2}},
		{Diff: change.Change{NewPath: "c.ts", Diff: "c1\nc2"}, Fragments: []unit.Fragment{c1, c2}},
		{Diff: change.Change{NewPath: "d.ts", Diff: "d1"}, Fragments: []unit.Fragment{d1}},
	}

	units := mergeCallChains(files, map[string][]string{
		"a.ts::a1": {"b.ts::b1"},
		"b.ts::b1": {"a.ts::a1"},
		"c.ts::c1": {"d.ts::d1"},
		"d.ts::d1": {"c.ts::c1"},
	}, unit.WatermarkMerger{Watermark: DefaultWatermark})
	if len(units) != 4 {
		t.Fatalf("want one useful chain plus three residual file Units, got %d: %+v", len(units), units)
	}
	if units[0].Scope != unit.ScopeCallChain || !strings.Contains(units[0].ID, "c1+d1") {
		t.Fatalf("useful c1/d1 chain was not retained: %+v", units[0])
	}
	for _, reviewUnit := range units {
		if reviewUnit.Scope == unit.ScopeCallChain && strings.Contains(reviewUnit.ID, "a1+b1") {
			t.Fatalf("expanding a1/b1 chain should have fallen back: %+v", reviewUnit)
		}
	}
}
