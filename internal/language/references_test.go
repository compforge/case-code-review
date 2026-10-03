package language

import (
	"os"
	"path/filepath"
	"testing"

	cg "github.com/compforge/codegraph"
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
		if n.Kind == cg.Reference && n.ReferenceKind == cg.SymbolReference && n.Location.Path == path && n.Name == name {
			count++
		}
	}
	return count
}
