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
	// Func and chain units have function names to walk from (a chain walks from
	// all member symbols; walkNeighbors seeds visited with them, so a member never
	// surfaces as another member's caller). File units would fan out over every
	// touched symbol — they degrade to nil. The graph uses the review repository.
	if f.RepoDir == "" || (u.Scope != unit.ScopeFunc && u.Scope != unit.ScopeCallChain) {
		return nil
	}
	emitSpec := f.Kinds.Spec && f.Index != nil
	if !emitSpec && !f.Kinds.Doc {
		return nil
	}
	// Own-spec short-circuit: a function with its own contract needs no inherited
	// one — this keeps a widely-called utility (huge fan-in) from exploding.
	if emitSpec {
		for _, sym := range u.AllSymbols() {
			if e, ok := f.Index[sym]; ok && (e.Spec != "" || len(e.Cases) > 0) {
				emitSpec = false
				break
			}
		}
		if !emitSpec && !f.Kinds.Doc {
			return nil
		}
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
	cfg := walkCfg{idx: f.Index, depth: f.Depth, max: max, spec: emitSpec, doc: doc}
	return walkNeighbors(cfg, u.AllSymbols(), f.callers, func(id string) unit.Clue {
		return unit.Clue{
			Kind:     unit.ClueSpec,
			Relation: unit.RelCaller,
			Text:     f.Index.Render([]string{id}),
			Ref:      id,
		}
	})
}

func (f CallerFinder) callers(funcID string) []string {
	analyzer := f.Analyzer
	if analyzer == nil {
		analyzer = language.NewAnalyzer(f.RepoDir)
	}
	return analyzer.Repository().CallNeighbors(funcID, true)
}
