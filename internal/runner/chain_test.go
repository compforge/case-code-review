package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func TestConnectedComponents(t *testing.T) {
	nodes := []string{"a", "b", "c", "d", "e"}
	adj := map[string][]string{ // a-b-c one component; d, e isolated
		"a": {"b"}, "b": {"a", "c"}, "c": {"b"},
	}
	comps := connectedComponents(nodes, adj)
	if len(comps) != 3 {
		t.Fatalf("want 3 components, got %d: %v", len(comps), comps)
	}
	if got := comps[0]; len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("want first component sorted [a b c], got %v", got)
	}
}

// ff / fn build test FileFragments / function Fragments.
func ff(path string, frags ...unit.Fragment) unit.FileFragments {
	return unit.FileFragments{Diff: change.Change{NewPath: path, Diff: "@@ -1 +1 @@\n+x"}, Fragments: frags}
}
func fn(path, sym string, lines int64) unit.Fragment {
	return unit.Fragment{Path: path, Symbols: []string{path + "::" + sym}, Diff: "@@\n+x", Insertions: lines}
}

func TestClusterByCallChainGroupsCrossFile(t *testing.T) {
	// a.go::A calls b.go::B (both changed) -> one cross-file chain Unit.
	// c.go::C is a lone changed func -> residual.
	files := []unit.FileFragments{
		ff("a.go", fn("a.go", "A", 3)),
		ff("b.go", fn("b.go", "B", 2)),
		ff("c.go", fn("c.go", "C", 1)),
	}
	adj := map[string][]string{"a.go::A": {"b.go::B"}, "b.go::B": {"a.go::A"}}

	chains, residual := clusterByCallChain(files, adj)
	if len(chains) != 1 {
		t.Fatalf("want 1 chain Unit, got %d", len(chains))
	}
	ch := chains[0]
	if ch.Scope != unit.ScopeCallChain {
		t.Errorf("want ScopeCallChain, got %v", ch.Scope)
	}
	if len(ch.Fragments) != 2 || len(ch.AllSymbols()) != 2 {
		t.Errorf("chain should cover both funcs, got fragments=%d symbols=%v", len(ch.Fragments), ch.AllSymbols())
	}
	if len(ch.Paths()) != 2 {
		t.Errorf("chain should span 2 files, got %v", ch.Paths())
	}
	if ids := funcIDsOf(residual); len(ids) != 1 || ids[0] != "c.go::C" {
		t.Errorf("residual should be [c.go::C], got %v", ids)
	}
}

func TestClusterByCallChainCapSkipsTooManyMembers(t *testing.T) {
	// 6 mutually-connected funcs > maxChainMembers(5) -> not clustered, all residual.
	names := []string{"A", "B", "C", "D", "E", "F"}
	var frags []unit.Fragment
	adj := map[string][]string{}
	for _, n := range names {
		frags = append(frags, fn("x.go", n, 1))
		if n != "A" {
			adj["x.go::A"] = append(adj["x.go::A"], "x.go::"+n) // star around A
			adj["x.go::"+n] = []string{"x.go::A"}
		}
	}
	chains, residual := clusterByCallChain([]unit.FileFragments{ff("x.go", frags...)}, adj)
	if len(chains) != 0 {
		t.Errorf("component of 6 exceeds member cap -> expect no chain, got %d", len(chains))
	}
	if len(funcIDsOf(residual)) != 6 {
		t.Errorf("all 6 funcs should fall back to residual, got %v", funcIDsOf(residual))
	}
}

func TestClusterByCallChainCapSkipsHighChurn(t *testing.T) {
	// 2 funcs within the member cap but 400 changed lines > maxChainChangeLines(300).
	files := []unit.FileFragments{ff("a.go", fn("a.go", "A", 200)), ff("b.go", fn("b.go", "B", 200))}
	adj := map[string][]string{"a.go::A": {"b.go::B"}, "b.go::B": {"a.go::A"}}

	chains, residual := clusterByCallChain(files, adj)
	if len(chains) != 0 {
		t.Errorf("400 changed lines > cap -> expect no chain, got %d", len(chains))
	}
	if len(funcIDsOf(residual)) != 2 {
		t.Errorf("both funcs should fall back to residual, got %v", funcIDsOf(residual))
	}
}
