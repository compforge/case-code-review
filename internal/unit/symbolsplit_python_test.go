package unit

import (
	"os/exec"
	"testing"

	"github.com/qiankunli/case-code-review/internal/diff"
)

func requirePython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

func TestAutoSplitter_PythonByFunctionAndMethod(t *testing.T) {
	requirePython3(t)
	src := `def alpha():
    return 1


class Svc:
    def do(self):
        return 2
`
	rawDiff := `diff --git a/p.py b/p.py
--- a/p.py
+++ b/p.py
@@ -2,1 +2,1 @@
-    return 0
+    return 1
@@ -7,1 +7,1 @@
-        return 0
+        return 2
`
	frags, err := AutoSplitter{}.Split(diff.Diff{NewPath: "p.py", Diff: rawDiff, NewFileContent: src})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 2 {
		t.Fatalf("want 2 fragments, got %d: %v", len(frags), ids(frags))
	}
	a := findFrag(t, frags, "p.py::alpha")
	if UnitOf(a).Scope != ScopeFunc || len(a.Symbols) != 1 || a.Symbols[0] != "p.py::alpha" {
		t.Errorf("alpha fields off: %+v", a)
	}
	// method id is Class.method
	findFrag(t, frags, "p.py::Svc.do")
}

func TestAutoSplitter_PythonSyntaxErrorFallsBack(t *testing.T) {
	requirePython3(t)
	frags, err := AutoSplitter{}.Split(diff.Diff{
		NewPath:        "p.py",
		Diff:           "@@ -1,1 +1,1 @@\n-a\n+b\n",
		NewFileContent: "def f(:\n  bad",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || UnitOf(frags[0]).Scope != ScopeFile {
		t.Fatalf("syntax error should fall back to one file fragment, got %v", ids(frags))
	}
}

func TestAutoSplitter_UnsupportedPathIsFileScope(t *testing.T) {
	frags, err := AutoSplitter{}.Split(diff.Diff{
		NewPath:        "a.txt",
		Diff:           "@@ -1,1 +1,1 @@\n-a\n+b\n",
		NewFileContent: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || UnitOf(frags[0]).Scope != ScopeFile {
		t.Fatalf("non-py should be one file fragment, got %v", ids(frags))
	}
}

func TestAutoSplitter_RoutesByExtension(t *testing.T) {
	// .txt -> file scope (no language splitter), regardless of python3 presence.
	frags, err := AutoSplitter{}.Split(diff.Diff{
		NewPath:        "notes.txt",
		Diff:           "@@ -1,1 +1,1 @@\n-a\n+b\n",
		NewFileContent: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || UnitOf(frags[0]).Scope != ScopeFile {
		t.Fatalf("txt should route to file scope, got %v", ids(frags))
	}
}

func TestAutoSplitter_RoutesTypeScript(t *testing.T) {
	frags, err := AutoSplitter{}.Split(diff.Diff{
		NewPath:        "app.ts",
		Diff:           "@@ -2,1 +2,1 @@\n-  return 0;\n+  return 1;\n",
		NewFileContent: "function app() {\n  return 1;\n}\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || UnitOf(frags[0]).Scope != ScopeFunc || frags[0].Symbols[0] != "app.ts::app" {
		t.Fatalf("TypeScript should route to function scope, got %v", ids(frags))
	}
}
