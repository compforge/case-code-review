package unitreview

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestParseHypothesesRequiresFalsifiableShape(t *testing.T) {
	hypotheses, errMsg := ParseHypotheses(map[string]any{
		"path": "a.go",
		"hypotheses": []any{
			map[string]any{
				"content":            "may return stale data",
				"existing_code":      "return cached",
				"trigger":            "cache entry expires during the request",
				"impact":             "caller receives stale data",
				"change_attribution": "the diff removed the expiry check",
				"evidence":           []any{"a.go:10 no longer checks expiresAt"},
				"uncertainty":        "whether callers tolerate stale values",
				"category":           "Bug",
				"severity":           " HIGH ",
			},
			map[string]any{"content": "only a suspicion"},
		},
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("got %d hypotheses, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	if h.ID == "" || h.Category != "bug" || h.Severity != "high" {
		t.Fatalf("unexpected hypothesis: %+v", h)
	}
}

func TestInvestigationToolDefsReplacePublicCommentTool(t *testing.T) {
	input := []llm.ToolDef{
		{Function: llm.FunctionDef{Name: "code_comment"}},
		{Function: llm.FunctionDef{
			Name: "post_bulletin", Description: "Use code_comment for local issues.",
		}},
	}
	defs := InvestigationToolDefs(input)
	if len(defs) != 2 || defs[0].Function.Name != ReportHypothesis.Name() {
		t.Fatalf("code_comment was not replaced: %+v", defs)
	}
	if defs[1].Function.Description != "Use report_hypothesis for local issues." {
		t.Fatalf("bulletin guidance was not updated: %q", defs[1].Function.Description)
	}
}
