package runner

import (
	"strings"
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
	enabledDescription := searchToolDescription(enabled)
	if !strings.HasPrefix(enabledDescription, "Required argument shape: "+searchCodeSymbolExample) {
		t.Fatalf("enabled search description does not lead with symbol context: %q", enabledDescription)
	}
	if !strings.Contains(enabledDescription, searchCodeSymbolGuidance) {
		t.Fatalf("enabled search description lacks symbol-context decision guidance: %q", enabledDescription)
	}

	disabled := BuildToolDefs(entries, false)
	originalDescription := searchToolDescription(disabled)
	ConfigureSearchSymbolContext(disabled, false)
	if searchToolHasProperty(disabled, "symbol_context") {
		t.Fatal("disabled symbol context remained in search_code schema")
	}
	disabledDescription := searchToolDescription(disabled)
	if disabledDescription != originalDescription {
		t.Fatalf("disabled search description changed: %q", disabledDescription)
	}
	if strings.Contains(disabledDescription, "symbol_context") {
		t.Fatalf("disabled search description advertises hidden symbol context: %q", disabledDescription)
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

func searchToolDescription(defs []llm.ToolDef) string {
	for _, definition := range defs {
		if definition.Function.Name == tool.CodeSearch.Name() {
			return definition.Function.Description
		}
	}
	return ""
}
