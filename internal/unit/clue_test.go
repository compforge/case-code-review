package unit

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// CoalesceFile is the cost governor's function→file rung: it merges a file's
// Fragments into one whole-file Unit, retaining every Fragment's symbols so spec
// context survives coarsening.
//
// Clue UNION is no longer CoalesceFile's job — clues are gathered post-merge, on
// the coalesced Unit's AllSymbols (the agent's findClues stage), not on each
// Fragment then unioned. So this test asserts the symbol union only; the
// post-merge clue gathering is covered by the agent package.
func TestCoalesceFileUnionsSymbols(t *testing.T) {
	d := change.Change{NewPath: "a.go", Diff: "@@ -1 +1 @@\n-a\n+b\n"}
	f1 := Fragment{Path: "a.go", Symbols: []string{"a.go::F1"}}
	f2 := Fragment{Path: "a.go", Symbols: []string{"a.go::F2"}}

	merged := CoalesceFile(d, []Fragment{f1, f2})
	if merged.Scope != ScopeFile {
		t.Errorf("coalesced unit should be file scope, got %v", merged.Scope)
	}
	if got := merged.AllSymbols(); len(got) != 2 {
		t.Errorf("want both symbols retained, got %v", got)
	}
	if merged.Diff() != d.Diff {
		t.Errorf("coalesced unit should review the whole-file diff, got %q", merged.Diff())
	}
}
