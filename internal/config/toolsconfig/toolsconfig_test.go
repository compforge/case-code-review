package toolsconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestBatchToolDescriptionsLeadWithCanonicalArguments(t *testing.T) {
	entries, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"read_files":      `{"reads":[{"file_path":"pkg/file.go","start_line":1,"end_line":80}]}`,
		"read_base_files": `{"reads":[{"file_path":"pkg/file.go","start_line":1,"end_line":80}]}`,
		"search_code":     `{"searches":[{"query":"Symbol","purpose":"reference","syntax":"literal","file_patterns":["*.go"],"context_lines":4}]}`,
	}
	seen := make(map[string]bool, len(want))
	for _, entry := range entries {
		example, ok := want[entry.Name]
		if !ok {
			continue
		}
		var def llm.FunctionDef
		if err := json.Unmarshal(entry.Definition, &def); err != nil {
			t.Fatalf("decode %s: %v", entry.Name, err)
		}
		if !strings.HasPrefix(def.Description, "Required argument shape: "+example) {
			t.Errorf("%s description does not lead with canonical arguments: %q", entry.Name, def.Description)
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(example), &args); err != nil {
			t.Errorf("%s canonical arguments are invalid JSON: %v", entry.Name, err)
		}
		seen[entry.Name] = true
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("default tool config is missing %s", name)
		}
	}
}
