package language

import "testing"

func TestExtractGoDoc(t *testing.T) {
	src := `package trace

// PhaseEventMiddleware accumulates phase events for one request.
// Per-request only — do not cache/reuse.
//
// Internal details beyond the first paragraph.
type PhaseEventMiddleware struct{}

// Dispatch handles one event.
func (m *PhaseEventMiddleware) Dispatch() {}

func Undocumented() {}
`
	if got := extractGoDoc(src, "PhaseEventMiddleware"); got != "PhaseEventMiddleware accumulates phase events for one request. Per-request only — do not cache/reuse." {
		t.Errorf("type doc = %q", got)
	}
	if got := extractGoDoc(src, "PhaseEventMiddleware.Dispatch"); got != "Dispatch handles one event." {
		t.Errorf("method doc = %q", got)
	}
	if got := extractGoDoc(src, "Undocumented"); got != "" {
		t.Errorf("undocumented should be empty, got %q", got)
	}
}

func TestExtractGoDoc_GroupedAndGeneric(t *testing.T) {
	src := `package x

type (
	// Grouped is declared inside a type block.
	Grouped struct{}

	Plain struct{}
)

// Single is a one-decl type block.
type Single struct{}

type Cache[K comparable] struct{}

// Get returns the value.
func (c *Cache[K]) Get() {}
`
	if got := extractGoDoc(src, "Grouped"); got != "Grouped is declared inside a type block." {
		t.Errorf("grouped type doc = %q", got)
	}
	if got := extractGoDoc(src, "Plain"); got != "" {
		t.Errorf("undocumented grouped type should be empty, got %q", got)
	}
	if got := extractGoDoc(src, "Single"); got != "Single is a one-decl type block." {
		t.Errorf("single type doc = %q", got)
	}
	if got := extractGoDoc(src, "Cache.Get"); got != "Get returns the value." {
		t.Errorf("generic-receiver method doc = %q", got)
	}
}

func TestAnalyzerDoc_Go(t *testing.T) {
	src := "package mw\n\n// Middleware is per-request only.\ntype Middleware struct{}\n"
	if got := NewAnalyzer("").Doc(Source{Path: "mw/trace.go", Content: src}, "Middleware"); got != "Middleware is per-request only." {
		t.Errorf("Analyzer.Doc(go) = %q", got)
	}
}

func extractGoDoc(src, name string) string {
	return NewAnalyzer("").Doc(Source{Path: "source.go", Content: src}, name)
}
