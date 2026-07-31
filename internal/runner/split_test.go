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

func TestSplitUnits_SingleFileCoalescesBelowWatermark(t *testing.T) {
	units := splitWith(t, goDiff("p.go", 3))
	if len(units) != 1 || units[0].Scope != unit.ScopeFile {
		t.Fatalf("want 1 file unit, got %d (%+v)", len(units), units)
	}
	if len(units[0].AllSymbols()) != 3 {
		t.Errorf("file unit should retain all 3 func ids, got %v", units[0].AllSymbols())
	}
}

func TestSplitUnits_MultipleFilesBelowWatermarkKeepFunctions(t *testing.T) {
	units := splitWith(t, goDiff("p.go", 2), goDiff("q.go", 1))
	if len(units) != 3 || countScope(units, unit.ScopeFunc) != 3 {
		t.Fatalf("want 3 function units, got %d (%d func)", len(units), countScope(units, unit.ScopeFunc))
	}
}

func TestSplitUnits_SingleFileRetainsSymbolsAboveWatermark(t *testing.T) {
	units := splitWith(t, goDiff("p.go", defaultUnitWatermark+2))
	if len(units) != 1 || units[0].Scope != unit.ScopeFile {
		t.Fatalf("want 1 coalesced file unit, got %d (%+v)", len(units), units)
	}
	if len(units[0].AllSymbols()) != defaultUnitWatermark+2 {
		t.Errorf("coalesced unit should retain all %d func ids, got %d", defaultUnitWatermark+2, len(units[0].AllSymbols()))
	}
}

func TestSplitUnits_GovernorCoarsensOnlyMultiFuncFiles(t *testing.T) {
	// 9 single-function files + 1 two-function file = 11 Units > budget(10).
	// Only the multi-function file coarsens: 9 func units + 1 coalesced file unit.
	var diffs []change.Change
	for i := range 9 {
		diffs = append(diffs, goDiff(fmt.Sprintf("f%d.go", i), 1))
	}
	diffs = append(diffs, goDiff("multi.go", 2))

	units := splitWith(t, diffs...)
	if len(units) != 10 {
		t.Fatalf("want 10 units, got %d", len(units))
	}
	if got := countScope(units, unit.ScopeFunc); got != 9 {
		t.Errorf("want 9 function units (single-func files stay func), got %d", got)
	}
	if got := countScope(units, unit.ScopeFile); got != 1 {
		t.Fatalf("want 1 coalesced file unit, got %d", got)
	}
	// the coalesced file unit retains both of multi.go's function ids
	for _, u := range units {
		if u.Scope == unit.ScopeFile {
			if len(u.AllSymbols()) != 2 {
				t.Errorf("coalesced multi.go unit should retain 2 func ids, got %v", u.AllSymbols())
			}
		}
	}
}
