package language

import (
	"context"
	cg "github.com/compforge/codegraph"
	"testing"
)

func TestGraphBudgetRetainsPrioritizedDocuments(t *testing.T) {
	a := NewAnalyzer("")
	var facts []cg.Facts
	for _, path := range []string{"changed.go", "background.go"} {
		f, err := a.extract(context.Background(), Source{Path: path, Content: "package p\nfunc F(){}\n"})
		if err != nil {
			t.Fatal(err)
		}
		facts = append(facts, f)
	}
	g, report, admitted, err := buildBoundedGraph(context.Background(), "test", facts, cg.Options{MaxDocuments: 1})
	if err != nil {
		t.Fatal(err)
	}
	if g == nil || admitted != 1 || len(report.Documents) != 1 || report.Documents[0] != "changed.go" {
		t.Fatalf("lost prioritized graph: admitted=%d report=%+v", admitted, report)
	}
	if _, ok := g.Node(cg.DocumentID("background.go")); ok {
		t.Fatal("omitted document is still published")
	}
}
