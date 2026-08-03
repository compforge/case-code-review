package unitreview

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestParseHypothesisRequiresFalsifiableShape(t *testing.T) {
	h, errMsg := ParseHypothesis(map[string]any{
		"path":               "a.go",
		"content":            "may return stale data",
		"existing_code":      "return cached",
		"trigger":            "cache entry expires during the request",
		"impact":             "caller receives stale data",
		"change_attribution": "the diff removed the expiry check",
		"evidence":           []any{"a.go:10 no longer checks expiresAt"},
		"uncertainty":        "whether callers tolerate stale values",
		"category":           "Bug",
		"severity":           " HIGH ",
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if h.ID != "" || h.Fingerprint == "" || h.Category != "bug" || h.Severity != "high" {
		t.Fatalf("unexpected hypothesis: %+v", h)
	}
}

func TestParseHypothesisRejectsIncompleteClaim(t *testing.T) {
	_, errMsg := ParseHypothesis(map[string]any{"path": "b.go", "content": "incomplete item"})
	if errMsg == "" {
		t.Fatal("an incomplete hypothesis must be rejected")
	}
}

func TestInvestigationToolDefsReplacePublicCommentTool(t *testing.T) {
	input := []llm.ToolDef{
		{Function: llm.FunctionDef{Name: "code_comment"}},
		{Function: llm.FunctionDef{Name: "task_done"}},
		{Function: llm.FunctionDef{
			Name: "post_bulletin", Description: "Use code_comment for local issues.",
		}},
	}
	defs := InvestigationToolDefs(input)
	if len(defs) != 2 || defs[0].Function.Name != SubmitHypothesis.Name() {
		t.Fatalf("code_comment was not replaced: %+v", defs)
	}
	if defs[1].Function.Description != "Use submit_hypothesis for local issues." {
		t.Fatalf("bulletin guidance was not updated: %q", defs[1].Function.Description)
	}
}

func TestHypothesisToolSchemaIsOneClaimWithoutBatchWrapper(t *testing.T) {
	properties := HypothesisToolDef().Function.Parameters["properties"].(map[string]any)
	if _, ok := properties["hypotheses"]; ok {
		t.Fatal("submit_hypothesis must not expose a batch wrapper")
	}
	if _, ok := properties["path"]; !ok {
		t.Fatal("single hypothesis fields must be direct tool arguments")
	}
}
