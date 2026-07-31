package unit

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func TestAutoSplitter_TreeSitterJavaMethod(t *testing.T) {
	fragments, err := AutoSplitter{}.Split(change.Change{
		NewPath: "Service.java",
		Diff: `diff --git a/Service.java b/Service.java
--- a/Service.java
+++ b/Service.java
@@ -2,3 +2,3 @@
   void run() {
-    oldValidate();
+    validate();
   }
`,
		NewFileContent: `class Service {
  void run() {
    validate();
  }
}
`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 || len(fragments[0].Symbols) != 1 || fragments[0].Symbols[0] != "Service.java::Service.run" {
		t.Fatalf("Java method should produce one function fragment, got %+v", fragments)
	}
}

func TestAutoSplitter_TreeSitterSyntaxErrorFallsBack(t *testing.T) {
	fragments, err := AutoSplitter{}.Split(change.Change{
		NewPath:        "Service.java",
		Diff:           "@@ -1,1 +1,1 @@\n-old\n+new\n",
		NewFileContent: "class Service { void run( {",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(fragments) != 1 || UnitOf(fragments[0]).Scope != ScopeFile {
		t.Fatalf("invalid Java should fall back to file scope, got %+v", fragments)
	}
}
