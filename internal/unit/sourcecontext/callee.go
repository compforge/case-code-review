package sourcecontext

import (
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

// CalleeFinder surfaces the contracts a changed function DEPENDS ON: it walks
// down to the function's callees (up to Depth hops), stopping each branch at the
// nearest spec-bearing callee and attaching its spec as a ClueCallee. Symmetric
// to CallerFinder (which walks up to the governing spec); this looks down to what
// the change relies on, so the reviewer can check the change still honours those
// callees' contracts. Bounded by Max/Depth, degrading to nil.
type CalleeFinder struct {
	RepoDir  string
	Index    spec.Index // may be nil: doc-only mode still works
	Analyzer *language.Analyzer
	Max      int
	Depth    int            // hops to walk down (0 -> default 2)
	Kinds    spec.KindGates // Spec: emit depended-on specs; Doc: emit direct callees' docstrings
}

func (f CalleeFinder) Find(u unit.Unit) []unit.Clue {
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
	index := f.Analyzer.Repository()
	nodes := u.GraphNodes(index)
	var doc *docRider
	if f.Kinds.Doc {
		doc = &docRider{analyzer: f.Analyzer, relation: unit.RelCallee}
	}
	cfg := walkCfg{graph: index, idx: f.Index, depth: f.Depth, max: max, spec: emitSpec, doc: doc}
	return walkNeighbors(cfg, nodes, f.callees, func(id string) unit.Clue {
		return unit.Clue{
			Kind:     unit.ClueSpec,
			Relation: unit.RelCallee,
			Text:     f.Index.Render([]string{index.ContractKey(id)}),
			Ref:      index.NodeLabel(id),
		}
	})
}

func (f CalleeFinder) callees(funcID string) []string {
	analyzer := f.Analyzer
	if analyzer == nil {
		analyzer = language.NewAnalyzer(f.RepoDir)
	}
	return analyzer.Repository().CallNodeNeighbors(funcID, false)
}
