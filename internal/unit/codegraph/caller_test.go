package codegraph

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

func TestCallerFinder_InheritsSpecFromCaller(t *testing.T) {
	repo := newRepo(t, map[string]string{
		"handler.go": "package p\n\nfunc Handle() { helper() }\n",
		"helper.go":  "package p\n\nfunc helper() {}\n",
	})
	idx, err := spec.Parse([]byte(`{"handler.go::Handle": {"spec": "must validate tenant before any write"}}`))
	if err != nil {
		t.Fatal(err)
	}

	// helper has no spec of its own -> walk up to its caller Handle (which does).
	u := unit.UnitOf(unit.Fragment{Path: "helper.go", Symbols: []string{"helper.go::helper"}})
	clues := CallerFinder{RepoDir: repo, Index: idx, Kinds: spec.KindGates{Spec: true}}.Find(u)

	if len(clues) != 1 {
		t.Fatalf("want 1 inherited caller clue, got %d: %+v", len(clues), clues)
	}
	if clues[0].Kind != unit.ClueSpec || clues[0].Relation != unit.RelCaller || clues[0].Ref != "handler.go::Handle" ||
		!strings.Contains(clues[0].Text, "must validate tenant") {
		t.Errorf("clue off: %+v", clues[0])
	}
}

func TestCallerFinder_ScopesUnexportedToPackage(t *testing.T) {
	// Two packages each define an unexported helper() called by a local handler.
	// Walking callers of a/helper must NOT reach b's handler — Go confines an
	// unexported helper's callers to its own package, so the spec inherited must
	// be a's, never b's same-named-helper caller.
	repo := newRepo(t, map[string]string{
		"a/handler.go": "package a\n\nfunc Handle() { helper() }\n",
		"a/helper.go":  "package a\n\nfunc helper() {}\n",
		"b/handler.go": "package b\n\nfunc OtherHandle() { helper() }\n",
		"b/helper.go":  "package b\n\nfunc helper() {}\n",
	})
	idx, err := spec.Parse([]byte(`{
		"a/handler.go::Handle": {"spec": "spec-A"},
		"b/handler.go::OtherHandle": {"spec": "spec-B"}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	u := unit.UnitOf(unit.Fragment{Path: "a/helper.go", Symbols: []string{"a/helper.go::helper"}})
	clues := CallerFinder{RepoDir: repo, Index: idx, Kinds: spec.KindGates{Spec: true}}.Find(u)

	if len(clues) != 1 {
		t.Fatalf("want exactly 1 same-package caller clue, got %d: %+v", len(clues), clues)
	}
	if clues[0].Ref != "a/handler.go::Handle" || !strings.Contains(clues[0].Text, "spec-A") {
		t.Errorf("should inherit package a's spec, got %+v", clues[0])
	}
	if strings.Contains(clues[0].Text, "spec-B") {
		t.Error("must not cross into package b's same-named helper caller")
	}
}

func TestCallerFinder_OwnSpecShortCircuits(t *testing.T) {
	idx, _ := spec.Parse([]byte(`{"helper.go::helper": {"spec": "own contract"}}`))
	// Own spec present -> no walk at all (no repo needed).
	u := unit.UnitOf(unit.Fragment{Path: "helper.go", Symbols: []string{"helper.go::helper"}})
	if clues := (CallerFinder{RepoDir: t.TempDir(), Index: idx, Kinds: spec.KindGates{Spec: true}}).Find(u); clues != nil {
		t.Errorf("own spec should short-circuit, got %+v", clues)
	}
}

func TestCallerFinder_Degrades(t *testing.T) {
	u := unit.UnitOf(unit.Fragment{Path: "a.go", Symbols: []string{"a.go::f"}})
	// no index / no repo
	if got := (CallerFinder{}).Find(u); got != nil {
		t.Errorf("no repo/index should degrade to nil, got %+v", got)
	}
	// file-scope unit has no function to walk from
	idx, _ := spec.Parse([]byte(`{"x.go::Caller": {"spec": "s"}}`))
	file := unit.UnitOf(unit.Fragment{Path: "a.go"})
	if got := (CallerFinder{RepoDir: t.TempDir(), Index: idx, Kinds: spec.KindGates{Spec: true}}).Find(file); got != nil {
		t.Errorf("file-scope unit should degrade to nil, got %+v", got)
	}
}

// newRepo creates a git repo (git grep needs one) with the given files and
// returns its path. It skips the test when git is unavailable.
func newRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo, err := os.MkdirTemp("", "ccr-codegraph-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// macOS can briefly keep Git's freshly-written object directories busy.
		// Retry cleanup instead of letting testing.TempDir report a false failure.
		for range 10 {
			if err := os.RemoveAll(repo); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Errorf("remove temporary git repository %s", repo)
	})
	for name, content := range files {
		p := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"config", "gc.auto", "0"},
		{"add", "-A"},
		{"commit", "-m", "x"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func TestCallerFinder_Depth2(t *testing.T) {
	// deepHelper (changed, no spec) <- mid (no spec) <- Entry (spec): two hops up.
	repo := newRepo(t, map[string]string{
		"f.go": "package p\n\nfunc deepHelper() error {\n\treturn nil\n}\n",
		"g.go": "package p\n\nfunc mid() error {\n\treturn deepHelper()\n}\n",
		"h.go": "package p\n\nfunc Entry() error {\n\treturn mid()\n}\n",
	})
	idx, err := spec.Parse([]byte(`{"h.go::Entry": {"spec": "the governing contract"}}`))
	if err != nil {
		t.Fatal(err)
	}
	u := unit.UnitOf(unit.Fragment{Path: "f.go", Symbols: []string{"f.go::deepHelper"}})

	// depth 2 walks deepHelper <- mid <- Entry and inherits Entry's spec.
	clues := CallerFinder{RepoDir: repo, Index: idx, Depth: 2, Kinds: spec.KindGates{Spec: true}}.Find(u)
	if len(clues) != 1 || clues[0].Ref != "h.go::Entry" {
		t.Fatalf("depth 2 should inherit Entry's spec, got %+v", clues)
	}
	// depth 1 stops at mid (no spec of its own) — nothing to inherit.
	if got := (CallerFinder{RepoDir: repo, Index: idx, Depth: 1, Kinds: spec.KindGates{Spec: true}}).Find(u); got != nil {
		t.Errorf("depth 1 should find nothing (mid has no spec), got %+v", got)
	}
}
