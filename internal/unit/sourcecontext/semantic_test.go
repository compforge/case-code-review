package sourcecontext

import (
	"context"
	cg "github.com/compforge/codegraph"
	"math"
	"testing"
)

// +case:id=semantic_rank_admission,expect=`Occurrence publication does not inflate definition ranking; calls, reads, writes and types remain usable`
func TestRankIndependentOfOccurrencePublication(t *testing.T) {
	docs := []cg.Document{
		{Path: "lib.go", Content: []byte("package app\nvar Count int\ntype Model struct{}\nfunc Target(){}\n")},
		{Path: "app.go", Content: []byte("package app\nfunc entry(m Model){Target(); _ = Target; Count += 1}\n")},
	}
	defs := map[string][]Def{"lib.go": {
		{Ident: "Count", File: "lib.go", SymbolID: "lib.go::Count", Line: 2},
		{Ident: "Model", File: "lib.go", SymbolID: "lib.go::Model", Line: 3},
		{Ident: "Target", File: "lib.go", SymbolID: "lib.go::Target", Line: 4},
	}}
	var baseline []RankedSymbol
	for _, admission := range []*cg.AdmissionPolicy{nil, cg.AnalysisAdmission()} {
		g, _, err := cg.Build(context.Background(), "rank", docs, cg.Options{Admission: admission})
		if err != nil {
			t.Fatal(err)
		}
		ranked := Rank(&Extraction{Defs: defs, Graph: g}, []string{"app.go"}, nil)
		if len(ranked) != 3 {
			t.Fatalf("missing semantic neighbors: %+v", ranked)
		}
		if baseline == nil {
			baseline = ranked
			continue
		}
		for i, got := range ranked {
			if got.Def != baseline[i].Def || math.Abs(got.Score-baseline[i].Score) > 1e-12 {
				t.Fatalf("occurrences affected rank: %+v != %+v", ranked, baseline)
			}
		}
	}
}
