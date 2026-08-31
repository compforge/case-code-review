package runner

import (
	"slices"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/toolsconfig"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func TestSearchToolExposesOnlyModelDecidableArguments(t *testing.T) {
	entries, err := toolsconfig.Load("")
	if err != nil {
		t.Fatal(err)
	}
	definitions := BuildToolDefs(entries, false)
	for _, definition := range definitions {
		if definition.Function.Name != tool.CodeSearch.Name() {
			continue
		}
		rootProperties := definition.Function.Parameters["properties"].(map[string]any)
		searches := rootProperties["searches"].(map[string]any)
		items := searches["items"].(map[string]any)
		properties := items["properties"].(map[string]any)
		got := make([]string, 0, len(properties))
		for name := range properties {
			got = append(got, name)
		}
		slices.Sort(got)
		want := []string{"case_sensitive", "file_patterns", "query", "syntax"}
		if !slices.Equal(got, want) {
			t.Fatalf("search item properties = %v, want %v", got, want)
		}
		required := items["required"].([]any)
		if len(required) != 1 || required[0] != "query" {
			t.Fatalf("search item required fields = %#v, want query only", required)
		}
		return
	}
	t.Fatal("search_code definition not found")
}
