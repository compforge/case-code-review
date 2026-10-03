package sourcecontext

import (
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

// CallerFinder walks up to a changed function's callers. With the spec kind on
// and a spec index present, it supplies the governing spec the function inherits
// (up to Depth hops, stopping each branch at the nearest spec-bearing ancestor):
// spec lives on entry functions (api-handlers) while a diff often lands on a
// deep helper, so the contract to preserve is the caller's. With the doc kind on
// it emits direct callers' docstrings (what context this function is used in) —
// doc is a derived mark, needing no spec.json; the two payloads are peers, each
// behind its own kind gate. Bounded by Max/Depth, degrading to nil on any miss.
type CallerFinder struct {
	RepoDir  string
	Index    spec.Index // may be nil: doc-only mode still works
	Analyzer *language.Analyzer
	Max      int            // cap on resolved spec-bearing callers (0 -> default)
	Depth    int            // hops to walk up (0 -> default 2)
	Kinds    spec.KindGates // Spec: emit inherited specs; Doc: emit direct callers' docstrings
}

func (f CallerFinder) Find(u unit.Unit) []unit.Clue {
	if len(u.AllSymbols()) == 0 {
		return nil
	}
	emitSpec := f.Kinds.Spec && f.Index != nil
	if !emitSpec && !f.Kinds.Doc {
		return nil
	}
	max := f.Max
	if max <= 0 {
		max = defaultMaxResults
	}
	if f.Analyzer == nil {
		f.Analyzer = language.NewAnalyzer(f.RepoDir)
	}
	var doc *docRider
	if f.Kinds.Doc {
		doc = &docRider{analyzer: f.Analyzer, relation: unit.RelCaller}
	}
	var starts []string
	for _, sym := range u.AllSymbols() {
		e := f.Index[sym]
		if e.Spec == "" && len(e.Cases) == 0 {
			starts = append(starts, sym)
		}
	}
	var clues []unit.Clue
	if emitSpec {
		clues = walkNeighbors(walkCfg{idx: f.Index, depth: f.Depth, max: max, spec: true, exclude: u.AllSymbols()}, starts, f.callers, func(id string) unit.Clue {
			return unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelCaller, Text: f.Index.Render([]string{id}), Ref: id}
		})
	}
	if doc != nil {
		clues = append(clues, walkNeighbors(walkCfg{max: max, doc: doc}, u.AllSymbols(), f.callers, nil)...)
	}
	return clues
}

func (f CallerFinder) callers(funcID string) []string {
	analyzer := f.Analyzer
	if analyzer == nil {
		analyzer = language.NewAnalyzer(f.RepoDir)
	}
	return analyzer.Repository().CallNeighbors(funcID, true)
}
