package codegraph

import "github.com/qiankunli/case-code-review/internal/gitcmd"

// CallAdjacency builds the undirected call adjacency among a set of CHANGED
// function symbol-ids: an edge between X and Y whenever one directly calls the
// other (one hop). It is the merge-time use of the call graph — "do these
// changed functions call each other", distinct from the clue-time use ("what
// governing spec / depended contract"). The merger groups call-adjacent changed
// functions into one review Unit so a requirement's change is reviewed along the
// call chain it touched.
//
// It reuses callee resolution (language facts + definition grep) and is therefore
// costly, so the caller gates it (only when the change stays function-grained).
// Backends without callee facts simply leave those functions unclustered.
func CallAdjacency(repoDir string, runner *gitcmd.Runner, typed *TypedGraph, funcIDs []string) map[string][]string {
	set := make(map[string]bool, len(funcIDs))
	for _, id := range funcIDs {
		set[id] = true
	}
	cf := CalleeFinder{RepoDir: repoDir, Runner: runner, Typed: typed} // Index unused by callees()
	adj := map[string][]string{}
	seen := map[[2]string]bool{}
	addEdge := func(a, b string) {
		key := [2]string{a, b}
		if a > b {
			key = [2]string{b, a}
		}
		if seen[key] {
			return
		}
		seen[key] = true
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}
	for _, x := range funcIDs {
		for _, y := range cf.callees(x) {
			if y != x && set[y] {
				addEdge(x, y)
			}
		}
	}
	return adj
}
