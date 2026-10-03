package language

import "testing"

func TestExtractPyDocstring(t *testing.T) {
	src := `class PhaseEventMiddleware:
    """Per-request only — do not cache/reuse.

    Accumulates events across a run.
    """
    def __init__(self): ...

def one_liner():
    'just one line'

def undocumented():
    return 1
`
	if got := NewAnalyzer("").Doc(Source{Path: "source.py", Content: src}, "PhaseEventMiddleware"); got != "Per-request only — do not cache/reuse." {
		t.Errorf("class docstring summary = %q", got)
	}
	if got := NewAnalyzer("").Doc(Source{Path: "source.py", Content: src}, "one_liner"); got != "just one line" {
		t.Errorf("one-liner = %q", got)
	}
	if got := NewAnalyzer("").Doc(Source{Path: "source.py", Content: src}, "undocumented"); got != "" {
		t.Errorf("undocumented should be empty, got %q", got)
	}
}

func TestDocumentationUsesDeclarationIdentity(t *testing.T) {
	source := Source{Path: "a.py", Content: "class A:\n    def run(self):\n        \"\"\"A contract.\"\"\"\nclass B:\n    def run(self):\n        \"\"\"B contract.\"\"\"\n"}
	a := NewAnalyzer("")
	for name, want := range map[string]string{"A.run": "A contract.", "B.run": "B contract.", "run": ""} {
		if got := a.Doc(source, name); got != want {
			t.Fatalf("%s: %q != %q", name, got, want)
		}
	}
	js := Source{Path: "a.ts", Content: "/** TypeScript contract. */\nexport function run() {}"}
	if got := a.Doc(js, "run"); got != "TypeScript contract." {
		t.Fatal(got)
	}
}
