package codegraph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture: three files — bed.go defines Manager.Resolve / Manager.Purge,
// web.go references them heavily, util.go is unrelated noise.
func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/bed/bed.go", `package bed

type Manager struct{}

func (m *Manager) Resolve(id string) error { return nil }

func (m *Manager) Purge(id string) error { return nil }
`)
	write("internal/web/web.go", `package web

func handleResolve() {
	var m Manager
	_ = m.Resolve("a")
	_ = m.Resolve("b")
	_ = m.Purge("c")
}
`)
	write("internal/util/util.go", `package util

func Helper() int { return 42 }
`)
	return dir
}

func TestScan_DefsAndSignatures(t *testing.T) {
	ex := Scan(writeFixture(t))
	defs := ex.Defs["internal/bed/bed.go"]
	if len(defs) != 3 { // type Manager + 2 methods
		t.Fatalf("expected 3 defs in bed.go, got %d: %+v", len(defs), defs)
	}
	var resolve *Def
	for i := range defs {
		if defs[i].Ident == "Manager.Resolve" {
			resolve = &defs[i]
		}
	}
	if resolve == nil {
		t.Fatal("Manager.Resolve def not found")
	}
	if resolve.SymbolID != "internal/bed/bed.go::Manager.Resolve" {
		t.Errorf("SymbolID = %q", resolve.SymbolID)
	}
	if !strings.Contains(resolve.Signature, "func (m *Manager) Resolve(id string) error") {
		t.Errorf("Signature = %q", resolve.Signature)
	}
}

func TestRank_SeedsPullRelatedSymbolsUp(t *testing.T) {
	ex := Scan(writeFixture(t))
	PairMethodIdents(ex)
	// The diff touches web.go and mentions Resolve — bed.go's defs must
	// outrank the unrelated util.go Helper.
	ranked := Rank(ex, []string{"internal/web/web.go"}, []string{"Resolve"})
	if len(ranked) == 0 {
		t.Fatal("no ranked symbols")
	}
	pos := map[string]int{}
	for i, rs := range ranked {
		if _, ok := pos[rs.Def.SymbolID]; !ok {
			pos[rs.Def.SymbolID] = i
		}
	}
	rp, rok := pos["internal/bed/bed.go::Manager.Resolve"]
	if !rok {
		t.Fatalf("Manager.Resolve not ranked: %+v", ranked)
	}
	if hp, ok := pos["internal/util/util.go::Helper"]; ok && hp < rp {
		t.Errorf("unrelated Helper (pos %d) outranked seeded Resolve (pos %d)", hp, rp)
	}
}

func TestBuildMap_BudgetAndContent(t *testing.T) {
	ex := Scan(writeFixture(t))
	PairMethodIdents(ex)
	m := BuildMap(ex, MapRequest{
		SeedFiles:  []string{"internal/web/web.go"},
		SeedIdents: []string{"Resolve"},
	})
	if m == "" {
		t.Fatal("empty map")
	}
	if !strings.Contains(m, "internal/bed/bed.go:") {
		t.Errorf("map missing defining file:\n%s", m)
	}
	if !strings.Contains(m, "Resolve") {
		t.Errorf("map missing seeded symbol:\n%s", m)
	}
	if estimateTokens(m) > DefaultMapTokens {
		t.Errorf("map exceeds default budget: %d tokens", estimateTokens(m))
	}

	tiny := BuildMap(ex, MapRequest{
		SeedFiles:    []string{"internal/web/web.go"},
		BudgetTokens: 40,
	})
	if estimateTokens(tiny) > 40 {
		t.Errorf("tiny budget exceeded: %d tokens\n%s", estimateTokens(tiny), tiny)
	}
}

func TestBuildMap_Deterministic(t *testing.T) {
	dir := writeFixture(t)
	req := MapRequest{SeedFiles: []string{"internal/web/web.go"}, SeedIdents: []string{"Resolve"}}
	ex1 := Scan(dir)
	PairMethodIdents(ex1)
	ex2 := Scan(dir)
	PairMethodIdents(ex2)
	if a, b := BuildMap(ex1, req), BuildMap(ex2, req); a != b {
		t.Errorf("non-deterministic map:\n--A--\n%s\n--B--\n%s", a, b)
	}
}

func TestRank_NoEdgesReturnsNil(t *testing.T) {
	ex := &Extraction{
		Defs: map[string][]Def{"a.go": {{Ident: "Lonely", File: "a.go", Line: 1}}},
		Refs: map[string]map[string]int{},
	}
	if got := Rank(ex, nil, nil); got != nil {
		t.Errorf("expected nil for edgeless graph, got %+v", got)
	}
}

func TestIsLikelySymbolName(t *testing.T) {
	for name, want := range map[string]bool{
		"Resolve":     true,
		"bed.Manager": true,
		"a":           false,
		"9lives":      false,
		"has space":   false,
	} {
		if got := IsLikelySymbolName(name); got != want {
			t.Errorf("IsLikelySymbolName(%q) = %v, want %v", name, got, want)
		}
	}
}
