package msg

import (
	"strings"
	"testing"
)

func TestFileContextKeepsViewsDistinct(t *testing.T) {
	context := NewFileContext([]FileContextEntry{
		{Path: "c.go", View: ViewReference, Reason: "repository_reference"},
		{Path: "a.go", View: ViewSource, Reason: "unit"},
		{Path: "b.go", View: ViewOutline, Reason: "callee", Ref: "b.go::B", Content: "File outline: b.go (go)\n- func B — L3-8"},
	})
	fullMessage := context.ToLLM(CompactionNone)
	full := fullMessage.ExtractText()
	if !strings.Contains(full, "[source] a.go") ||
		!strings.Contains(full, "[outline] b.go — callee (b.go::B)") ||
		!strings.Contains(full, "- func B") ||
		!strings.Contains(full, "[reference] c.go") {
		t.Fatalf("full context = %q", full)
	}
	condensedMessage := context.ToLLM(CompactionCondensed)
	condensed := condensedMessage.ExtractText()
	if strings.Contains(condensed, "- func B") || !strings.Contains(condensed, "[reference] b.go") {
		t.Fatalf("condensed context = %q", condensed)
	}
	items := context.ContextItems(CompactionCondensed)
	if len(items) != 3 || items[1].Identity != "b.go" || items[1].Representation != "reference" {
		t.Fatalf("condensed context items = %#v", items)
	}
}
