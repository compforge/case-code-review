package unit

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/diff"
)

func TestAutoSplitter_TypeScriptFunctionMethodAndArrow(t *testing.T) {
	src := `export function alpha() {
  return helper();
}

class Service {
  run() {
    return validate();
  }
}

const View = () => {
  return <span>ok</span>;
};
`
	rawDiff := `diff --git a/app.tsx b/app.tsx
--- a/app.tsx
+++ b/app.tsx
@@ -2,1 +2,1 @@
-  return oldHelper();
+  return helper();
@@ -7,1 +7,1 @@
-    return oldValidate();
+    return validate();
@@ -12,1 +12,1 @@
-  return <span>old</span>;
+  return <span>ok</span>;
`
	frags, err := AutoSplitter{}.Split(diff.Diff{NewPath: "app.tsx", Diff: rawDiff, NewFileContent: src})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 3 {
		t.Fatalf("want 3 function fragments, got %d: %v", len(frags), ids(frags))
	}
	findFrag(t, frags, "app.tsx::alpha")
	findFrag(t, frags, "app.tsx::Service.run")
	view := findFrag(t, frags, "app.tsx::View")
	if !strings.Contains(view.Diff, "<span>ok</span>") {
		t.Fatalf("View diff not isolated:\n%s", view.Diff)
	}
}

func TestAutoSplitter_TypeScriptDoesNotNeedNode(t *testing.T) {
	t.Setenv("PATH", "")
	frags, err := AutoSplitter{}.Split(diff.Diff{
		NewPath:        "app.ts",
		Diff:           "@@ -1,1 +1,1 @@\n-old\n+new\n",
		NewFileContent: "function app() { return 1; }",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || UnitOf(frags[0]).Scope != ScopeFunc || frags[0].Symbols[0] != "app.ts::app" {
		t.Fatalf("TypeScript should use in-process parsing without Node, got %v", ids(frags))
	}
}
