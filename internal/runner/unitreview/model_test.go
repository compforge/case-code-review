package unitreview

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestParseHypothesesRequiresFalsifiableShape(t *testing.T) {
	hypotheses, errMsg := ParseHypotheses(map[string]any{
		"hypotheses": []any{
			map[string]any{
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
			},
		},
	})
	if errMsg != "" {
		t.Fatal(errMsg)
	}
	if len(hypotheses) != 1 {
		t.Fatalf("got %d hypotheses, want 1", len(hypotheses))
	}
	h := hypotheses[0]
	if h.ID != "" || h.Fingerprint == "" || h.Category != "bug" || h.Severity != "high" {
		t.Fatalf("unexpected hypothesis: %+v", h)
	}
}

func TestParseHypothesesAcceptsExplicitEmptySubmission(t *testing.T) {
	hypotheses, errMsg := ParseHypotheses(map[string]any{"hypotheses": []any{}})
	if errMsg != "" || len(hypotheses) != 0 {
		t.Fatalf("empty hypothesis batch = %+v, %q", hypotheses, errMsg)
	}
}

func TestParseHypothesesRejectsWholeInvalidBatch(t *testing.T) {
	_, errMsg := ParseHypotheses(map[string]any{"hypotheses": []any{
		map[string]any{
			"path": "a.go", "content": "valid-looking item",
			"existing_code": "return cached", "trigger": "expired entry", "impact": "stale result",
			"change_attribution": "expiry check removed", "evidence": []any{"a.go:10"},
			"uncertainty": "", "category": "bug", "severity": "high",
		},
		map[string]any{"path": "b.go", "content": "incomplete item"},
	}})
	if errMsg == "" {
		t.Fatal("an invalid item must reject the entire batch")
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
	if len(defs) != 2 || defs[0].Function.Name != SubmitHypotheses.Name() {
		t.Fatalf("code_comment was not replaced: %+v", defs)
	}
	if defs[1].Function.Description != "Use submit_hypotheses for local issues." {
		t.Fatalf("bulletin guidance was not updated: %q", defs[1].Function.Description)
	}
}
