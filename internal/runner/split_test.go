package runner

import (
	"fmt"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// goDiff builds a change.Change for a Go file of n trivial functions (f0..f{n-1})
// with a one-line change in each, so AutoSplitter yields n function Units.
func goDiff(path string, n int) change.Change {
	var s strings.Builder
	s.WriteString("package p\n\n")
	for i := range n {
		fmt.Fprintf(&s, "func f%d() {\n\t_ = %d\n}\n\n", i, i)
	}
	var d strings.Builder
	fmt.Fprintf(&d, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", path, path, path, path)
	for i := range n {
		start := 3 + i*4 // each func block + trailing blank is 4 lines, first at line 3
		fmt.Fprintf(&d, "@@ -%d,3 +%d,3 @@\n func f%d() {\n-\t_ = old\n+\t_ = %d\n }\n", start, start, i, i)
	}
	return change.Change{NewPath: path, Diff: d.String(), NewFileContent: s.String(), Insertions: int64(n), Deletions: int64(n)}
}

func splitWith(t *testing.T, diffs ...change.Change) []unit.Unit {
	t.Helper()
	a := &Runner{splitter: unit.AutoSplitter{}, changes: diffs}
	units, err := a.splitUnits()
	if err != nil {
		t.Fatal(err)
	}
	return units
}

func countScope(units []unit.Unit, s unit.Scope) int {
	n := 0
	for _, u := range units {
		if u.Scope == s {
			n++
		}
	}
	return n
}

func TestSplitUnits_IndependentChangesInOneFileRemainSeparate(t *testing.T) {
	units := splitWith(t, goDiff("p.go", 3))
	if len(units) != 3 {
		t.Fatalf("want 3 independent changes, got %d", len(units))
	}
	seen := map[string]bool{}
	for _, u := range units {
		if seen[u.ID] {
			t.Fatal("duplicate Unit identity")
		}
		seen[u.ID] = true
	}
}

func TestSplitUnits_MultipleFilesBelowWatermarkKeepFunctions(t *testing.T) {
	units := splitWith(t, goDiff("p.go", 2), goDiff("q.go", 1))
	if len(units) != 3 || countScope(units, unit.ScopeFunc) != 3 {
		t.Fatalf("want 3 function units, got %d (%d func)", len(units), countScope(units, unit.ScopeFunc))
	}
}

func TestSplitUnits_LargeChangeDoesNotDiscardSymbolScopes(t *testing.T) {
	units := splitWith(t, goDiff("p.go", 12))
	if len(units) != 12 {
		t.Fatalf("want 12 independent changes, got %d", len(units))
	}
	symbols := map[string]bool{}
	for _, u := range units {
		for _, s := range u.AllSymbols() {
			symbols[s] = true
		}
	}
	if len(symbols) != 12 {
		t.Fatalf("lost target symbols: %v", symbols)
	}
}
