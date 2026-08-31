package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/toolsconfig"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestConfigureSearchSymbolContextMatchesFeatureGate(t *testing.T) {
	entries, err := toolsconfig.Load("")
	if err != nil {
		t.Fatal(err)
	}

	enabled := BuildToolDefs(entries, false)
	ConfigureSearchSymbolContext(enabled, true)
	if !searchToolHasProperty(enabled, "symbol_context") {
		t.Fatal("enabled symbol context was removed from search_code schema")
	}

	disabled := BuildToolDefs(entries, false)
	ConfigureSearchSymbolContext(disabled, false)
	if searchToolHasProperty(disabled, "symbol_context") {
		t.Fatal("disabled symbol context remained in search_code schema")
	}
}

func searchToolHasProperty(defs []llm.ToolDef, property string) bool {
	for _, definition := range defs {
		if definition.Function.Name != tool.CodeSearch.Name() {
			continue
		}
		rootProperties, _ := definition.Function.Parameters["properties"].(map[string]any)
		searches, _ := rootProperties["searches"].(map[string]any)
		items, _ := searches["items"].(map[string]any)
		properties, _ := items["properties"].(map[string]any)
		_, ok := properties[property]
		return ok
	}
	return false
}
