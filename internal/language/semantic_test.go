package language

import (
	cg "github.com/compforge/codegraph/pkg"
	"testing"
)

// +case:id=semantic_review_navigation,expect=`Usage counts source occurrences once while review connections retain semantic operations and declaration owners`
func TestSemanticReviewNavigation(t *testing.T) {
	a := referenceRepo(t, map[string]string{"app.go": "package app\nvar count int\ntype Model struct{}\nfunc handler(){}\nfunc register(f func()){}\nfunc entry(m Model){handler(); register(handler); count += 1; _ = count}\n"})
	index := a.Repository()
	for symbol, want := range map[string]int{"app.go::handler": 2, "app.go::count": 3} {
		uses := index.UsesOf(symbol)
		if len(uses) != want {
			t.Fatalf("%s uses=%d want=%d: %+v", symbol, len(uses), want, uses)
		}
		for _, n := range uses {
			if !n.Kind.IsOccurrence() || ReviewSymbolID(n) != "" {
				t.Fatalf("not an occurrence: %+v", n)
			}
		}
	}
	owners := graphAnchors(index.Graph, "app.go")
	touched := map[cg.RelationKind]bool{}
	for _, c := range index.Connections(owners, "app.go", []Span{{Start: 6, End: 6}}) {
		n, _ := index.Graph.Node(c.Source)
		if n.Kind.IsOccurrence() || c.Kind == cg.BindsTo {
			t.Fatalf("navigation leaked: %+v", c)
		}
		if c.Touched {
			touched[c.Kind] = true
		}
	}
	for _, kind := range []cg.RelationKind{cg.Calls, cg.Reads, cg.Writes, cg.UsesType} {
		if !touched[kind] {
			t.Errorf("missing touched %s: %v", kind, touched)
		}
	}
}
