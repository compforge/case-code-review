package codegraph

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/stdout"
)

// typedBuildTimeout caps the one-off packages.Load. A module that can't
// type-check inside it isn't worth blocking the review for — grep fallback.
const typedBuildTimeout = 60 * time.Second

// TypedGraph is the lazily-built, once-per-review handle to the typed Go call
// graph. All consumers (caller/callee finders, merge adjacency) share one
// instance so the packages.Load cost is paid at most once — and only when a
// Go symbol actually asks for neighbors. A nil handle, a failed build, or a
// non-Go symbol all mean "answer with the grep heuristics instead".
type TypedGraph struct {
	RepoDir string
	once    sync.Once
	g       *language.CallGraph
}

func (t *TypedGraph) graph() *language.CallGraph {
	if t == nil || t.RepoDir == "" {
		return nil
	}
	t.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), typedBuildTimeout)
		defer cancel()
		start := time.Now()
		g, err := language.BuildGoCallGraph(ctx, t.RepoDir)
		if err != nil {
			// Expected on non-building diffs / non-Go repos — informational.
			// Stderr, not stdout: the lazy build fires inside dry-run too,
			// and --format json must stay machine-parseable.
			fmt.Fprintf(stdout.Err(), "[ccr] Typed call graph unavailable (%v) — using grep heuristics\n", err)
			return
		}
		nodes, edges := g.Stats()
		fmt.Fprintf(stdout.Err(), "[ccr] Typed call graph: %d funcs, %d edges in %s\n",
			nodes, edges, time.Since(start).Round(time.Millisecond))
		t.g = g
	})
	return t.g
}

// Callers answers from the typed graph when it applies to this symbol.
// ok=false means "no typed answer — fall back", which is distinct from
// (nil, true) = "typed graph says: no callers".
func (t *TypedGraph) Callers(funcID string) ([]string, bool) {
	g, ok := t.applies(funcID)
	if !ok {
		return nil, false
	}
	return g.Callers(funcID), true
}

// Callees mirrors Callers.
func (t *TypedGraph) Callees(funcID string) ([]string, bool) {
	g, ok := t.applies(funcID)
	if !ok {
		return nil, false
	}
	return g.Callees(funcID), true
}

// applies reports whether the typed graph can authoritatively answer for this
// symbol: the graph built AND the symbol is Go. For Go symbols the graph's
// answer is trusted even when empty (the type checker saw the whole module);
// Python and friends always fall back.
func (t *TypedGraph) applies(funcID string) (*language.CallGraph, bool) {
	path, _, ok := language.SplitSymbolID(funcID)
	lang, supported := language.Detect(path)
	if !ok || !supported || lang != language.Go {
		return nil, false
	}
	g := t.graph()
	if g == nil {
		return nil, false
	}
	return g, true
}
