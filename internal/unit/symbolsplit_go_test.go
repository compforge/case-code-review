package unit

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/diff"
)

// fragID is the symbol-id a fragment represents: its sole symbol for a function
// fragment, or its path for a residual/whole-file fragment.
func fragID(f Fragment) string {
	if len(f.Symbols) >= 1 {
		return f.Symbols[0]
	}
	return f.Path
}

func ids(frags []Fragment) []string {
	out := make([]string, len(frags))
	for i, f := range frags {
		out[i] = fragID(f)
	}
	return out
}

// findFrag returns the fragment with the given symbol-id (or path, for the
// residual), or fails.
func findFrag(t *testing.T, frags []Fragment, id string) Fragment {
	t.Helper()
	for _, f := range frags {
		if fragID(f) == id {
			return f
		}
	}
	t.Fatalf("no fragment with id %q; got %v", id, ids(frags))
	return Fragment{}
}

func TestAutoSplitter_GoByFunction(t *testing.T) {
	src := `package foo

import "fmt"

func Alpha() {
	fmt.Println("alpha changed")
}

func Beta(x int) int {
	return x + 1
}
`
	rawDiff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -5,3 +5,3 @@
 func Alpha() {
-	fmt.Println("alpha")
+	fmt.Println("alpha changed")
 }
@@ -9,3 +9,3 @@
 func Beta(x int) int {
-	return x
+	return x + 1
 }
`
	frags, err := AutoSplitter{}.Split(diff.Diff{NewPath: "foo.go", Diff: rawDiff, NewFileContent: src})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 2 {
		t.Fatalf("want 2 func fragments, got %d: %v", len(frags), ids(frags))
	}

	alpha := findFrag(t, frags, "foo.go::Alpha")
	if UnitOf(alpha).Scope != ScopeFunc || alpha.Path != "foo.go" ||
		len(alpha.Symbols) != 1 || alpha.Symbols[0] != "foo.go::Alpha" {
		t.Errorf("alpha fragment fields off: %+v", alpha)
	}
	if alpha.Insertions != 1 || alpha.Deletions != 1 {
		t.Errorf("alpha counts: +%d/-%d, want +1/-1", alpha.Insertions, alpha.Deletions)
	}
	if !strings.Contains(alpha.Diff, "alpha changed") || strings.Contains(alpha.Diff, "x + 1") {
		t.Errorf("alpha diff should slice only Alpha's hunk:\n%s", alpha.Diff)
	}

	beta := findFrag(t, frags, "foo.go::Beta")
	if beta.Insertions != 1 || beta.Deletions != 1 {
		t.Errorf("beta counts: +%d/-%d", beta.Insertions, beta.Deletions)
	}
}

func TestAutoSplitter_GoMethodAndResidual(t *testing.T) {
	src := `package foo

import (
	"fmt"
	"errors"
)

func (s *Service) Do() error {
	return errors.New("x")
}
`
	_ = src
	rawDiff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -3,3 +3,4 @@
 import (
 	"fmt"
+	"errors"
 )
@@ -8,3 +8,3 @@
 func (s *Service) Do() error {
-	return nil
+	return errors.New("x")
 }
`
	frags, err := AutoSplitter{}.Split(diff.Diff{NewPath: "foo.go", Diff: rawDiff, NewFileContent: src})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 2 {
		t.Fatalf("want 1 method + 1 residual, got %d: %v", len(frags), ids(frags))
	}

	// pointer receiver stripped to "Service"
	m := findFrag(t, frags, "foo.go::Service.Do")
	if UnitOf(m).Scope != ScopeFunc {
		t.Errorf("method fragment should map to func scope, got %v", UnitOf(m).Scope)
	}

	// residual is the symbol-less fragment (maps to file scope) carrying the import hunk
	var residual *Fragment
	for i := range frags {
		if len(frags[i].Symbols) == 0 {
			residual = &frags[i]
		}
	}
	if residual == nil {
		t.Fatal("expected a residual (symbol-less) fragment for the import change")
	}
	if UnitOf(*residual).Scope != ScopeFile {
		t.Errorf("residual should map to file scope, got %v", UnitOf(*residual).Scope)
	}
	if !strings.Contains(residual.Diff, "errors") || strings.Contains(residual.Diff, "return") {
		t.Errorf("residual should hold only the import hunk:\n%s", residual.Diff)
	}
}

func TestAutoSplitter_GoFallsBackOnParseError(t *testing.T) {
	frags, err := AutoSplitter{}.Split(diff.Diff{
		NewPath:        "bad.go",
		Diff:           "@@ -1,1 +1,1 @@\n-x\n+y\n",
		NewFileContent: "package foo\nthis is not valid go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || UnitOf(frags[0]).Scope != ScopeFile {
		t.Fatalf("parse error should fall back to one file fragment, got %v", ids(frags))
	}
}

func TestAutoSplitter_UnparseablePythonIsFileScope(t *testing.T) {
	frags, err := AutoSplitter{}.Split(diff.Diff{
		NewPath:        "app/api.py",
		Diff:           "@@ -1,1 +1,1 @@\n-x\n+y\n",
		NewFileContent: "def f(: bad",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || UnitOf(frags[0]).Scope != ScopeFile {
		t.Fatalf("unparseable source should be one file fragment, got %v", ids(frags))
	}
}
