package hypothesisreview

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
)

func TestToolDefsAreConvergent(t *testing.T) {
	names := []string{"task_done", "code_comment", "post_bulletin", "read_files", "read_base_files", "search_code"}
	var input []llm.ToolDef
	for _, name := range names {
		input = append(input, llm.ToolDef{Function: llm.FunctionDef{Name: name}})
	}
	defs := ToolDefs(input)
	got := map[string]bool{}
	for _, def := range defs {
		got[def.Function.Name] = true
	}
	if got["code_comment"] || got["post_bulletin"] || got[unitreview.SubmitHypothesis.Name()] {
		t.Fatalf("convergent review exposed divergent tools: %v", got)
	}
	if got["task_done"] {
		t.Fatalf("Review 2 exposed redundant completion tool: %v", got)
	}
	for _, required := range []string{"read_files", "read_base_files", "search_code", SubmitAssessment.Name()} {
		if !got[required] {
			t.Errorf("missing review tool %q: %v", required, got)
		}
	}
}

func TestAssessmentHookBindsCurrentHypothesisAndLastValidWins(t *testing.T) {
	collector := NewAssessmentCollector("h-1")
	var submissions []AssessmentSubmission
	hook := &AssessmentHook{
		Collector: collector, LaneID: "l-1",
		OnAccepted: func(submission AssessmentSubmission) { submissions = append(submissions, submission) },
	}
	call := func(value string) map[string]any {
		checkpoint, handled := hook.HandleTool(context.Background(), harness.ToolRequest{
			Tool: SubmitAssessment,
			Args: map[string]any{
				"support": "insufficient", "attribution": "unknown",
				"value": value, "novelty": "new", "reason": "evidence is incomplete", "evidence": []any{},
			},
		})
		if !handled {
			t.Fatal("submit_assessment was not handled")
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(checkpoint.Data), &result); err != nil {
			t.Fatalf("tool result is not JSON: %q", checkpoint.Data)
		}
		return result
	}

	first := call("unknown")
	if first["accepted"] != "h-1" || first["replaced"] != false {
		t.Fatalf("first submission = %+v", first)
	}
	second := call("low_value")
	if second["accepted"] != "h-1" || second["replaced"] != true {
		t.Fatalf("replacement submission = %+v", second)
	}

	assessments := collector.Assessments()
	if len(assessments) != 1 || !collector.Complete() {
		t.Fatalf("collector = %+v", assessments)
	}
	if assessments[0].HypothesisID != "h-1" || assessments[0].Value != LowValue || assessments[0].SubmissionIndex != 2 {
		t.Fatalf("last valid assessment did not win: %+v", assessments[0])
	}
	if assessments[0].LaneID != "l-1" {
		t.Fatalf("assessment lane = %q, want l-1", assessments[0].LaneID)
	}
	if len(submissions) != 2 || !submissions[1].Replaced {
		t.Fatalf("submission trail = %+v", submissions)
	}
}

func TestAssessmentToolSchemaDoesNotExposeHypothesisIdentityOrBatch(t *testing.T) {
	properties := AssessmentToolDef().Function.Parameters["properties"].(map[string]any)
	if _, ok := properties["hypothesis_id"]; ok {
		t.Fatal("hypothesis identity must be bound by the current Review 2 execution")
	}
	if _, ok := properties["assessments"]; ok {
		t.Fatal("submit_assessment must not expose a batch wrapper")
	}
}
