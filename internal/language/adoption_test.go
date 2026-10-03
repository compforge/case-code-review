package language

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzerKeepsDocumentVersionsAndDetachedViews(t *testing.T) {
	analyzer := NewAnalyzer("")
	first := Source{Path: "source.py", Content: "def before():\n    pass\n"}
	second := Source{Path: first.Path, Content: "def after():\n    pass\n"}
	a, err := analyzer.Analyze(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	a.Definitions[0].SymbolID = "mutated"
	for _, test := range []struct {
		source Source
		name   string
	}{{second, "after"}, {first, "before"}} {
		got, err := analyzer.Analyze(context.Background(), test.source)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got.DefinitionByID("source.py::" + test.name); !ok {
			t.Fatalf("mixed document versions: %+v", got)
		}
		outline, err := analyzer.FileOutline(context.Background(), test.source)
		if err != nil || !strings.Contains(outline.Render(), test.name) {
			t.Fatalf("outline/version mismatch: %s %v", outline.Render(), err)
		}
	}
}

func TestRepositorySnapshotIgnoresWorktreeChanges(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, src string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, path), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q")
	write("entry.py", "from helper import helper\ndef entry():\n    return helper()\n")
	write("helper.py", "def helper():\n    \"\"\"Snapshot contract.\"\"\"\n    return 1\n")
	git("add", "entry.py", "helper.py")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "commit", "-qm", "fixture")
	commit := git("rev-parse", "HEAD")
	write("entry.py", "def entry():\n    return 0\n")
	if err := os.Remove(filepath.Join(dir, "helper.py")); err != nil {
		t.Fatal(err)
	}
	write("extra.py", "def helper():\n    return 99\n")
	analyzer := NewSnapshotAnalyzer(dir, commit, nil)
	index := analyzer.Repository()
	refs := analyzer.ReferencesAt("entry.py", []Span{{3, 3}})
	if len(refs) != 1 || refs[0].SymbolID != "helper.py::helper" {
		t.Fatalf("used relation escaped snapshot: %+v", refs)
	}
	if doc := analyzer.RepositoryDoc("helper.py::helper"); doc != "Snapshot contract." {
		t.Fatalf("documentation escaped snapshot: %q", doc)
	}
	if index.Graph == nil || index.Graph.Snapshot() != commit || len(index.Gaps) > 0 {
		t.Fatalf("snapshot not built: %+v", index)
	}
	if got := index.CallNeighbors("entry.py::entry", false); !reflect.DeepEqual(got, []string{"helper.py::helper"}) {
		t.Fatalf("snapshot calls = %v", got)
	}
	if doc := analyzer.RepositoryDoc("helper.py::helper"); doc != "Snapshot contract." {
		t.Fatalf("snapshot doc = %q", doc)
	}
	if _, ok := index.Sources["extra.py"]; ok {
		t.Fatal("untracked worktree file entered historical snapshot")
	}
	if analyzer.Repository() != index {
		t.Fatal("graph was rebuilt within the same review")
	}
	workspace := NewAnalyzer(dir).Repository()
	if got := workspace.CallNeighbors("entry.py::entry", false); len(got) != 0 {
		t.Fatalf("workspace used historical graph: %v", got)
	}
}

func TestRepositoryRejectsNameOnlyCallCandidates(t *testing.T) {
	dir := t.TempDir()
	for path, source := range map[string]string{
		"entry.py":  "def entry():\n    return helper()\n",
		"helper.py": "def helper():\n    return 1\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	index := NewAnalyzer(dir).Repository()
	if index.Graph == nil {
		t.Fatalf("missing graph: %+v", index.Gaps)
	}
	if got := index.CallNeighbors("entry.py::entry", false); len(got) != 0 {
		t.Fatalf("unbound call promoted to reliable edge: %v", got)
	}
	if got := index.CallNeighbors("helper.py::helper", true); len(got) != 0 {
		t.Fatalf("unbound caller promoted to reliable edge: %v", got)
	}
}

func TestRepositoryRetainsPartialCoverage(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Run.java"), []byte("class Run { void run() { missing(); } }"), 0o644); err != nil {
		t.Fatal(err)
	}
	index := NewAnalyzer(dir).Repository()
	if index.Graph == nil || len(index.Definitions["Run.java"]) == 0 || len(index.Report.Diagnostics) == 0 {
		t.Fatalf("partial declaration coverage lost: %+v", index)
	}
}

func TestRepositorySeparatesReceiversAndRejectsAmbiguousJoinKeys(t *testing.T) {
	dir := t.TempDir()
	for path, source := range map[string]string{
		"methods.go":   "package p\ntype A struct{}\ntype B struct{}\nfunc (a A) Save() {}\nfunc (b B) Save() {}\nfunc Use(a A) { a.Save() }\n",
		"duplicate.py": "def helper():\n    return 1\ndef helper():\n    return 2\ndef entry():\n    return helper()\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	index := NewAnalyzer(dir).Repository()
	if got := index.CallNeighbors("methods.go::Use", false); !reflect.DeepEqual(got, []string{"methods.go::A.Save"}) {
		t.Fatalf("receiver binding = %v", got)
	}
	if got := index.CallNeighbors("methods.go::B.Save", true); len(got) != 0 {
		t.Fatalf("wrong receiver caller = %v", got)
	}
	if got := index.CallNeighbors("duplicate.py::entry", false); len(got) != 0 {
		t.Fatalf("ambiguous CCR identity admitted = %v", got)
	}
}
