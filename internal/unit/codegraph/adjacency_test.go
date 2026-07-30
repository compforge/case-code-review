package codegraph

import (
	"slices"
	"testing"
)

func TestCallAdjacency(t *testing.T) {
	// A calls B; C calls nothing in the changed set. Among {A,B,C} the only
	// adjacency is the undirected A-B edge.
	repo := newRepo(t, map[string]string{
		"a.go": "package p\n\nfunc A() { B() }\n",
		"b.go": "package p\n\nfunc B() {}\n",
		"c.go": "package p\n\nfunc C() {}\n",
	})
	adj := CallAdjacency(repo, nil, nil, []string{"a.go::A", "b.go::B", "c.go::C"})

	if !slices.Contains(adj["a.go::A"], "b.go::B") || !slices.Contains(adj["b.go::B"], "a.go::A") {
		t.Errorf("want undirected A-B edge, got %v", adj)
	}
	if len(adj["c.go::C"]) != 0 {
		t.Errorf("C calls nothing in the set, want no edges, got %v", adj["c.go::C"])
	}
}

func TestCallAdjacencyIgnoresCallsOutsideSet(t *testing.T) {
	// A calls B, but B is not in the changed set -> no edge (only changed funcs
	// cluster; an unchanged callee is context, not a merge member).
	repo := newRepo(t, map[string]string{
		"a.go": "package p\n\nfunc A() { B() }\n",
		"b.go": "package p\n\nfunc B() {}\n",
	})
	adj := CallAdjacency(repo, nil, nil, []string{"a.go::A"})
	if len(adj["a.go::A"]) != 0 {
		t.Errorf("B not in set -> A should have no edges, got %v", adj["a.go::A"])
	}
}
