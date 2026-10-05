package language

import (
	"context"
	cg "github.com/compforge/codegraph"
	"os"
	"path/filepath"
	"testing"
)

func TestCommonNamespacesPreservesGraphProofAndSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\nfunc A(){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	index := NewAnalyzer(dir).Repository()
	matches, err := index.CommonNamespaces(t.Context(), nil, "a.go")
	if err != nil || len(matches) != 1 {
		t.Fatalf("matches=%v err=%v", matches, err)
	}
	for ref, match := range matches {
		if ref.Snapshot != index.Graph.Snapshot() || ref.NodeID != match.Node.ID || len(match.Paths) != 1 {
			t.Fatalf("lost identity/proof: %+v %+v", ref, match)
		}
		proof := match.Paths[0]
		if len(proof.Relations) != 1 || proof.Relations[0].Kind != cg.InNamespace || proof.Nodes[0].ID != cg.DocumentID("a.go") {
			t.Fatalf("lost document ownership: %+v", proof)
		}
	}
	anchors := []Anchor{{Snapshot: "another-version", NodeID: cg.DocumentID("a.go")}}
	if _, err := index.CommonNamespaces(context.Background(), anchors, "a.go"); err == nil {
		t.Fatal("mixed graph snapshots")
	}
	matches, err = index.CommonNamespaces(t.Context(), nil, "missing.go")
	if err != nil || len(matches) != 0 {
		t.Fatalf("unknown document was inferred: %v %v", matches, err)
	}
}
