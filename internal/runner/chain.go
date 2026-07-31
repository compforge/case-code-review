package runner

import (
	"fmt"
	"sort"

	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// Call-chain cluster caps bound a ScopeCallChain Unit so one review loop isn't
// swamped. A connected component exceeding either cap is NOT clustered — its
// members fall back to per-function units and thus to the cost axis — so a big
// fan-out refactor degrades to file coarsening instead of one giant Unit.
const (
	maxChainMembers     = 5
	maxChainChangeLines = 300
)

// funcIDsOf collects the symbol-ids of the function fragments (exactly one symbol)
// across the files — the node set for call-chain adjacency.
func funcIDsOf(files []unit.FileFragments) []string {
	var ids []string
	for fi := range files {
		for _, f := range files[fi].Fragments {
			if len(f.Symbols) == 1 {
				ids = append(ids, f.Symbols[0])
			}
		}
	}
	return ids
}

// clusterByCallChain groups call-adjacent changed FUNCTIONS (possibly across
// files) into ScopeCallChain Units — reviewing a requirement's change along the
// call chain it touched — given the precomputed undirected adjacency among the
// changed function ids. It returns those Units plus the residual FileFragments
// (everything not clustered) for the cost-axis merger. Pure: the costly call-graph
// query that produces adj is the caller's job (see CallAdjacency), keeping the
// clustering + cap logic testable on its own.
func clusterByCallChain(files []unit.FileFragments, adj map[string][]string) ([]unit.Unit, []unit.FileFragments) {
	fragByID := map[string]unit.Fragment{}
	var funcIDs []string
	for fi := range files {
		for _, f := range files[fi].Fragments {
			if len(f.Symbols) == 1 {
				fragByID[f.Symbols[0]] = f
				funcIDs = append(funcIDs, f.Symbols[0])
			}
		}
	}

	clustered := map[string]bool{}
	var chains []unit.Unit
	for _, comp := range connectedComponents(funcIDs, adj) {
		if len(comp) < 2 {
			continue // a lone function is not a chain
		}
		var lines int64
		for _, id := range comp {
			lines += fragByID[id].Insertions + fragByID[id].Deletions
		}
		if len(comp) > maxChainMembers || lines > maxChainChangeLines {
			// Too big for one loop — leave its members to the cost axis. Logged so
			// the coarsening isn't silent.
			fmt.Fprintf(console.Out(), "[ccr] call-chain cluster of %d funcs (%d lines) exceeds cap — left to function/file scope: %v\n", len(comp), lines, comp)
			continue
		}
		frags := make([]unit.Fragment, 0, len(comp))
		for _, id := range comp {
			frags = append(frags, fragByID[id])
			clustered[id] = true
		}
		chains = append(chains, unit.NewChainUnit(frags))
	}

	if len(clustered) == 0 {
		return nil, files
	}
	// Residual: every fragment except the clustered function fragments.
	var residual []unit.FileFragments
	for fi := range files {
		keep := make([]unit.Fragment, 0, len(files[fi].Fragments))
		for _, f := range files[fi].Fragments {
			if len(f.Symbols) == 1 && clustered[f.Symbols[0]] {
				continue
			}
			keep = append(keep, f)
		}
		if len(keep) > 0 {
			residual = append(residual, unit.FileFragments{Diff: files[fi].Diff, Fragments: keep})
		}
	}
	return chains, residual
}

// connectedComponents returns the connected components of the undirected graph
// (nodes + adjacency). Nodes are visited in sorted order and each component is
// sorted, so the resulting chain Units are deterministic.
func connectedComponents(nodes []string, adj map[string][]string) [][]string {
	sorted := append([]string(nil), nodes...)
	sort.Strings(sorted)

	visited := map[string]bool{}
	var comps [][]string
	for _, n := range sorted {
		if visited[n] {
			continue
		}
		var comp []string
		queue := []string{n}
		visited[n] = true
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			comp = append(comp, cur)
			for _, nb := range adj[cur] {
				if !visited[nb] {
					visited[nb] = true
					queue = append(queue, nb)
				}
			}
		}
		sort.Strings(comp)
		comps = append(comps, comp)
	}
	return comps
}
