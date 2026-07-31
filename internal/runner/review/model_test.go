package review

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

func TestTrialRequiresEveryDeliveryGateAndDiffReceipt(t *testing.T) {
	approved := Hypothesis{Path: "a.go", Content: "real issue", ExistingCode: "x"}
	approved.ID = IDFor(approved)
	lowValue := Hypothesis{Path: "a.go", Content: "minor issue", ExistingCode: "y"}
	lowValue.ID = IDFor(lowValue)
	missing := Hypothesis{Path: "a.go", Content: "unreviewed", ExistingCode: "z"}
	missing.ID = IDFor(missing)

	findings := Trial(
		[]Hypothesis{approved, approved, lowValue, missing},
		[]Assessment{
			{
				HypothesisID: approved.ID, Support: Supported,
				Attribution: Caused, Value: Actionable, Novelty: Novel,
				Evidence:         []string{"a.go:10"},
				EvidenceReceipts: []EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
			},
			{
				HypothesisID: lowValue.ID, Support: Supported,
				Attribution: Caused, Value: LowValue, Novelty: Novel,
				Evidence:         []string{"a.go:20"},
				EvidenceReceipts: []EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
			},
		},
	)
	if len(findings) != 1 || findings[0].Content != approved.Content {
		t.Fatalf("unexpected findings: %+v", findings)
	}
}

func TestHypothesisReviewToolDefsAreConvergent(t *testing.T) {
	defs := []struct {
		name string
	}{
		{"task_done"}, {"code_comment"}, {"post_bulletin"}, {"file_read"},
		{"file_read_base"}, {"code_search"},
	}
	var input []llm.ToolDef
	for _, def := range defs {
		input = append(input, llm.ToolDef{Function: llm.FunctionDef{Name: def.name}})
	}
	reviewDefs := HypothesisReviewToolDefs(input)
	got := map[string]bool{}
	for _, def := range reviewDefs {
		got[def.Function.Name] = true
	}
	if got["code_comment"] || got["post_bulletin"] || got[ReportHypothesis.Name()] {
		t.Fatalf("convergent review exposed divergent tools: %v", got)
	}
	for _, required := range []string{
		"task_done", "file_read", "file_read_base", "code_search", SubmitAssessments.Name(),
	} {
		if !got[required] {
			t.Errorf("missing review tool %q: %v", required, got)
		}
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
