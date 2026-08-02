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

func TestAssessmentHookAcceptsPartialBatchesAndLastValidWins(t *testing.T) {
	collector := NewAssessmentCollector("h-1", "h-2")
	var submissions []AssessmentSubmission
	hook := &AssessmentHook{
		Collector: collector, LaneID: "l-1",
		OnAccepted: func(submission AssessmentSubmission) { submissions = append(submissions, submission) },
	}
	call := func(id, value string) map[string]any {
		checkpoint, handled := hook.HandleTool(context.Background(), harness.ToolRequest{
			Tool: SubmitAssessments,
			Args: map[string]any{"assessments": []any{map[string]any{
				"hypothesis_id": id, "support": "insufficient", "attribution": "unknown",
				"value": value, "novelty": "new", "reason": "evidence is incomplete", "evidence": []any{},
			}}},
		})
		if !handled {
			t.Fatal("submit_assessments was not handled")
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(checkpoint.Data), &result); err != nil {
			t.Fatalf("tool result is not JSON: %q", checkpoint.Data)
		}
		return result
	}

	first := call("h-1", "unknown")
	if remaining := first["remaining"].([]any); len(remaining) != 1 || remaining[0] != "h-2" {
		t.Fatalf("remaining after partial submit = %+v", remaining)
	}
	call("h-1", "low_value")
	rejected := call("not-in-review", "unknown")
	if got := rejected["rejected_unknown"].([]any); len(got) != 1 || got[0] != "not-in-review" {
		t.Fatalf("unknown IDs = %+v", got)
	}
	call("h-2", "unknown")

	assessments := collector.Assessments()
	if len(assessments) != 2 || !collector.Complete() {
		t.Fatalf("collector = %+v, missing = %v", assessments, collector.Missing())
	}
	if assessments[0].HypothesisID != "h-1" || assessments[0].Value != LowValue || assessments[0].SubmissionIndex != 2 {
		t.Fatalf("last valid assessment did not win: %+v", assessments[0])
	}
	if assessments[0].LaneID != "l-1" {
		t.Fatalf("assessment lane = %q, want l-1", assessments[0].LaneID)
	}
	if len(submissions) != 3 || !submissions[1].Replaced {
		t.Fatalf("submission trail = %+v", submissions)
	}
}
