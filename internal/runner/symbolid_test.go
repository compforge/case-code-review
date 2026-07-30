package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/diff"
	"github.com/qiankunli/case-code-review/internal/finding"
	"github.com/qiankunli/case-code-review/internal/language"
)

func TestTagSymbolIDs(t *testing.T) {
	// Foo spans lines 3-5; a comment on line 4 resolves to svc.go::Foo.
	src := "package p\n\nfunc Foo() error {\n\treturn doX()\n}\n"
	a := &Runner{diffs: []diff.Diff{{NewPath: "svc.go", NewFileContent: src}}, analyzer: language.NewAnalyzer("")}

	comments := []finding.Finding{
		{Path: "svc.go", StartLine: 4},   // inside Foo -> tagged
		{Path: "other.go", StartLine: 1}, // no loaded diff -> left untagged
	}
	a.tagSymbolIDs(comments)

	if comments[0].SymbolID != "svc.go::Foo" {
		t.Errorf("want svc.go::Foo, got %q", comments[0].SymbolID)
	}
	if comments[1].SymbolID != "" {
		t.Errorf("comment with no diff should stay untagged, got %q", comments[1].SymbolID)
	}
}
