package language

import (
	"os"
	"path/filepath"
	"testing"

	cgmodel "github.com/compforge/codegraph/pkg"
)

func referenceRepo(t *testing.T, files map[string]string) *Analyzer {
	t.Helper()
	dir := t.TempDir()
	for path, src := range files {
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return NewAnalyzer(dir)
}

func TestReferencesFollowImportsAndReexports(t *testing.T) {
	a := referenceRepo(t, map[string]string{
		"lib.ts":       "export function target() {}",
		"barrel.ts":    "export { target as publicName } from './lib';",
		"app.ts":       "import { publicName as local } from './barrel';\nfunction run(){local()}\n",
		"unrelated.ts": "export function local() {}",
	})
	refs := a.ReferencesAt("app.ts", []Span{{2, 2}})
	if len(refs) != 1 || refs[0].SymbolID != "lib.ts::target" {
		t.Fatalf("alias chain: %+v", refs)
	}
	uses := a.Repository().UsesOf("lib.ts::target")
	found := false
	for _, n := range uses {
		if n.Location.Path == "app.ts" && n.Location.Line == 2 {
			found = true
		}
	}
	if !found {
		t.Fatal("reverse aliases lost source uses", uses)
	}
}

func TestReferencesRejectCommentsStringsAndShadowedNames(t *testing.T) {
	a := referenceRepo(t, map[string]string{
		"lib.py": "def target():\n    pass\n",
		"app.py": "from lib import target\n# target()\ntext = 'target()'\ndef run(target):\n    return target()\n",
	})
	if refs := a.ReferencesAt("app.py", []Span{{2, 5}}); len(refs) != 0 {
		t.Fatalf("nonbinding text acquired targets: %+v", refs)
	}
}

func TestReferencesPreserveProvenExternalImport(t *testing.T) {
	a := referenceRepo(t, map[string]string{"app.py": "from external.module import Target as Alias\ndef run():\n    return Alias()\n"})
	refs := a.ReferencesAt("app.py", []Span{{3, 3}})
	if len(refs) != 1 || refs[0].Name != "Alias" || refs[0].FQN != "external.module.Target" || refs[0].SymbolID != "" {
		t.Fatal(refs)
	}
}

func TestOwnersUseCrossFileReceiverMembership(t *testing.T) {
	a := referenceRepo(t, map[string]string{"type.go": "package p\ntype S struct{}\n", "method.go": "package p\nfunc(S) Run(){}\n"})
	owners := a.Repository().Owners("method.go::S.Run")
	if len(owners) != 1 || owners[0] != "type.go::S" {
		t.Fatal(owners)
	}
}

func referenceCount(index *RepositoryIndex, path, name string) int {
	count := 0
	for _, n := range index.Graph.Nodes() {
		if n.Kind.IsOccurrence() && n.Location.Path == path && n.Name == name {
			count++
		}
	}
	return count
}

// +case:id=semantic_occurrence_navigation,desc=`Read/write/type/call occurrences preserve navigation and declaration ownership`,expect=`Usage sites stay distinct from dependency edges and review targets`
// +link=component://docs/language.md
func TestSemanticOccurrencesKeepNavigationAndReviewOwnership(t *testing.T) {
	a := referenceRepo(t, map[string]string{
		"model.go": "package p\ntype Widget struct{}\nvar Shared int\nfunc Target() {}\n",
		"use.go":   "package p\nfunc Use(w Widget) {\n Shared = Shared + 1\n Target()\n}\n",
	})
	index := a.Repository()
	refs := a.ReferencesAt("use.go", []Span{{2, 4}})
	targets := map[string]bool{}
	for _, ref := range refs {
		targets[ref.SymbolID] = true
	}
	for _, id := range []string{"model.go::Widget", "model.go::Shared", "model.go::Target"} {
		if !targets[id] {
			t.Errorf("missing bound target %s: %+v", id, refs)
		}
	}
	uses := index.UsesOf("model.go::Shared")
	kinds := map[cgmodel.NodeKind]int{}
	for _, use := range uses {
		kinds[use.Kind]++
	}
	if len(uses) != 2 || kinds[cgmodel.ReadSite] != 1 || kinds[cgmodel.WriteSite] != 1 {
		t.Fatalf("read/write source sites: %+v", uses)
	}
	if calls := index.UsesOf("model.go::Target"); len(calls) != 1 || calls[0].Kind != cgmodel.CallSite {
		t.Fatalf("call dependency counted as an extra usage: %+v", calls)
	}
	for _, anchor := range graphAnchors(index.Graph, "use.go") {
		if anchor.Kind.IsOccurrence() {
			t.Fatalf("occurrence became review target: %+v", anchor)
		}
	}
	for _, node := range index.Graph.Nodes() {
		if node.Kind.IsOccurrence() && ReviewSymbolID(node) != "" {
			t.Fatalf("occurrence acquired declaration identity: %+v", node)
		}
	}
}
