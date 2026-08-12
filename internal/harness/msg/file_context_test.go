package msg

import (
	"strings"
	"testing"
)

func TestFileContextKeepsViewsDistinct(t *testing.T) {
	context := NewFileContext([]FileContextEntry{
		{Path: "c.go", View: ViewReference, Reason: "repository_reference"},
		{Path: "b.go", View: ViewOutline, Reason: "callee", Ref: "b.go::B", Content: "File outline: b.go (go)\n- func B — L3-8"},
	})
	fullMessage := context.ToLLM()
	full := fullMessage.ExtractText()
	if !strings.Contains(full, "[outline] b.go — callee (b.go::B)") ||
		!strings.Contains(full, "- func B") ||
		!strings.Contains(full, "[reference] c.go") {
		t.Fatalf("full context = %q", full)
	}
	projected, _ := context.Compact(0)
	condensedContext := projected.(*FileContext)
	condensedMessage := condensedContext.ToLLM()
	condensed := condensedMessage.ExtractText()
	if strings.Contains(condensed, "- func B") || !strings.Contains(condensed, "[reference] b.go") {
		t.Fatalf("condensed context = %q", condensed)
	}
	items := condensedContext.ContextItems()
	if len(items) != 2 || items[0].Identity != "b.go" || items[0].Representation != "reference" {
		t.Fatalf("condensed context items = %#v", items)
	}
}
