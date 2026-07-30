package codegraph

import (
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

const defaultDepth = 2

// neighborFunc returns the call-graph neighbors of a function (its callers, or
// its callees) as symbol-ids. CallerFinder and CalleeFinder differ only in this.
type neighborFunc func(funcID string) []string

// docRider configures the depth-1 neighbor docstring emission that rides on the
// walk (nil = off — the doc kind gate, or no repo to read from).
type docRider struct {
	repoDir  string
	relation unit.Relation
}

// walkCfg bundles a neighbor walk's knobs (params were sprawling).
type walkCfg struct {
	idx        spec.Index // local spec index; may be nil in doc-only mode
	depth, max int
	spec       bool      // emit nearest spec-bearing neighbors (the walk's payload)
	doc        *docRider // depth-1 docstring rider (nil = off)
}

// walkNeighbors walks outward from start along neighborFn and emits two kinds of
// payload, each independently switchable:
//
//   - spec (cfg.spec): a clue (via mkClue) for the NEAREST spec-bearing neighbor
//     on each branch, up to cfg.depth hops — a spec-bearing neighbor is emitted
//     and not recursed past; one with no spec of its own is followed one hop
//     further. This is the caller (walk up to the governing spec) / callee (walk
//     down to depended-on contracts) engine. Meaningful because authored spec is
//     SPARSE — "walk until spec" terminates on signal.
//
//   - doc (cfg.doc): the docstring of each *direct* (depth-1) neighbor, reusing
//     neighbors the walk already computed (no extra grep). doc is a mark like
//     spec — just derived from source instead of authored, so it needs no
//     spec.json. The payloads are peers; the asymmetry is density, not rank:
//     docstrings are DENSE (nearly every symbol has one), so there is nothing to
//     "walk until" — beyond depth-1 they'd be noise. Hence doc-only walks one hop.
//
// visited breaks cycles and dedupes; cfg.max caps each payload separately so
// docs never crowd out specs.
func walkNeighbors(cfg walkCfg, start []string, neighborFn neighborFunc, mkClue func(id string) unit.Clue) []unit.Clue {
	depth := cfg.depth
	if depth <= 0 {
		depth = defaultDepth
	}
	if !cfg.spec {
		depth = 1 // doc-only: direct neighbors carry the whole payload
	}
	visited := map[string]bool{}
	for _, s := range start {
		visited[s] = true
	}
	frontier := append([]string(nil), start...)

	var specClues, docClues []unit.Clue
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []string
		for _, f := range frontier {
			for _, nb := range neighborFn(f) {
				if visited[nb] {
					continue
				}
				visited[nb] = true
				if d == 0 && cfg.doc != nil && len(docClues) < cfg.max {
					if text := spec.SymbolDocstring(cfg.doc.repoDir, nb); text != "" {
						docClues = append(docClues, unit.Clue{
							Kind:     unit.ClueDoc,
							Relation: cfg.doc.relation,
							Text:     text,
							Ref:      nb,
						})
					}
				}
				if cfg.spec {
					if e, ok := cfg.idx[nb]; ok && (e.Spec != "" || len(e.Cases) > 0) {
						specClues = append(specClues, mkClue(nb))
						if len(specClues) >= cfg.max {
							return append(specClues, docClues...)
						}
						continue // nearest spec on this branch — don't recurse past it
					}
				}
				next = append(next, nb) // no spec yet — follow one hop further
			}
		}
		frontier = next
	}
	return append(specClues, docClues...)
}
