package sourcecontext

import (
	"context"
	"math"
	"testing"

	cg "github.com/compforge/codegraph"
)

// +case:id=semantic_rank_admission,desc=`The same declarations and dependencies are published with and without occurrence nodes`,expect=`Calls, reads, writes and type dependencies produce identical definition ranking`
// +link=component://docs/language.md
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
	for _, profile := range []struct {
		name        string
		admission   *cg.AdmissionPolicy
		occurrences bool
	}{{"full", nil, true}, {"analysis", cg.AnalysisAdmission(), false}} {
		t.Run(profile.name, func(t *testing.T) {
			graph, _, err := cg.Build(context.Background(), "rank", docs, cg.Options{Admission: profile.admission})
			if err != nil {
				t.Fatal(err)
			}
			occurrences := 0
			for _, node := range graph.Nodes() {
				if node.Kind.IsOccurrence() {
					occurrences++
				}
			}
			if (occurrences > 0) != profile.occurrences {
				t.Fatalf("profile did not select the expected occurrence publication: %d", occurrences)
			}
			ranked := Rank(&Extraction{Defs: defs, Graph: graph}, []string{"app.go"}, nil)
			if len(ranked) != 3 {
				t.Fatalf("missing semantic neighbors: %+v", ranked)
			}
			for _, symbol := range ranked {
				if symbol.Score <= 0 {
					t.Fatalf("dependency has no positive relevance: %+v", symbol)
				}
			}
			if baseline == nil {
				baseline = ranked
				return
			}
			for i, got := range ranked {
				if got.Def != baseline[i].Def || math.Abs(got.Score-baseline[i].Score) > 1e-12 {
					t.Fatalf("occurrences affected rank: %+v != %+v", ranked, baseline)
				}
			}
		})
	}
}
