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

func TestMergeCallChainsCoalescesOnlyResidualFragments(t *testing.T) {
	a1 := unit.Fragment{Path: "a.ts", Symbols: []string{"a.ts::a1"}, Diff: "a1"}
	a2 := unit.Fragment{Path: "a.ts", Symbols: []string{"a.ts::a2"}, Diff: "a2"}
	aResidual := unit.Fragment{Path: "a.ts", Diff: "imports"}
	b1 := unit.Fragment{Path: "b.ts", Symbols: []string{"b.ts::b1"}, Diff: "b1"}
	b2 := unit.Fragment{Path: "b.ts", Symbols: []string{"b.ts::b2"}, Diff: "b2"}
	files := []unit.FileFragments{
		{Diff: change.Change{NewPath: "a.ts"}, Fragments: []unit.Fragment{a1, a2, aResidual}},
		{Diff: change.Change{NewPath: "b.ts"}, Fragments: []unit.Fragment{b1, b2}},
	}

	units := mergeCallChains(files, map[string][]string{
		"a.ts::a1": {"b.ts::b1"},
		"b.ts::b1": {"a.ts::a1"},
	}, unit.WatermarkMerger{Watermark: DefaultWatermark})
	if len(units) != 3 {
		t.Fatalf("want chain plus two residual Units, got %d: %+v", len(units), units)
	}
	if units[0].Scope != unit.ScopeCallChain {
		t.Fatalf("first Unit = %s, want callchain", units[0].Scope)
	}
	if units[1].Scope != unit.ScopeFile || len(units[1].Fragments) != 2 {
		t.Fatalf("a.ts residuals were not coalesced: %+v", units[1])
	}
	if got := units[1].Diff(); strings.Contains(got, "a1") || !strings.Contains(got, "a2") || !strings.Contains(got, "imports") {
		t.Fatalf("coalesced residual diff reintroduced chain content: %q", got)
	}
	if units[2].Scope != unit.ScopeFunc || units[2].ID != "b.ts#b2" {
		t.Fatalf("single b.ts residual changed shape: %+v", units[2])
	}
}
