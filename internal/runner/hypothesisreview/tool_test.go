package hypothesisreview

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
)

func TestToolDefsAreConvergent(t *testing.T) {
	names := []string{"task_done", "code_comment", "post_bulletin", "file_read", "file_read_base", "code_search"}
	var input []llm.ToolDef
	for _, name := range names {
		input = append(input, llm.ToolDef{Function: llm.FunctionDef{Name: name}})
	}
	defs := ToolDefs(input)
	got := map[string]bool{}
	for _, def := range defs {
		got[def.Function.Name] = true
	}
	if got["code_comment"] || got["post_bulletin"] || got[unitreview.ReportHypothesis.Name()] {
		t.Fatalf("convergent review exposed divergent tools: %v", got)
	}
	for _, required := range []string{"task_done", "file_read", "file_read_base", "code_search", SubmitAssessments.Name()} {
		if !got[required] {
			t.Errorf("missing review tool %q: %v", required, got)
		}
	}
}
