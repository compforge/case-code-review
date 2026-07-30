package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/runner/feature"
)

// New should assemble clue finders per the feature gates: the RelatedFinder
// (self/owner/used; kind gates applied inside it — see spec.KindGates tests) plus
// history when on, and the two costly ones (caller/callee) when on.
func TestNew_ClueGatesControlFinderAssembly(t *testing.T) {
	full := New(Args{}) // nil Features → all gates on
	if len(full.finders) != 2 {
		t.Errorf("all-on: want 2 cheap finders (related + history), got %d", len(full.finders))
	}
	if len(full.costlyFinders) != 2 {
		t.Errorf("all-on: want 2 costly finders (caller/callee), got %d", len(full.costlyFinders))
	}

	off, err := feature.Resolve(map[feature.Gate]bool{
		feature.CallerCallee: false,
		feature.History:      false,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := New(Args{Features: off})
	if len(a.costlyFinders) != 0 {
		t.Errorf("caller_callee off: want 0 costly finders, got %d", len(a.costlyFinders))
	}
	if len(a.finders) != 1 { // related only (history gated off)
		t.Errorf("history off: want 1 cheap finder, got %d", len(a.finders))
	}

	// caller/callee emit two peer payloads (spec / doc): with spec off but doc on
	// they still assemble (doc-only, depth-1 neighbor docstrings — no spec.json
	// needed); with both kinds off there is nothing to emit, so they drop.
	noSpec, err := feature.Resolve(map[feature.Gate]bool{feature.SpecCase: false})
	if err != nil {
		t.Fatal(err)
	}
	if b := New(Args{Features: noSpec}); len(b.costlyFinders) != 2 {
		t.Errorf("spec_case off, doc on: want 2 costly finders (doc-only mode), got %d", len(b.costlyFinders))
	}
	neither, err := feature.Resolve(map[feature.Gate]bool{feature.SpecCase: false, feature.Doc: false})
	if err != nil {
		t.Fatal(err)
	}
	if c := New(Args{Features: neither}); len(c.costlyFinders) != 0 {
		t.Errorf("spec+doc off: want 0 costly finders, got %d", len(c.costlyFinders))
	}
}
