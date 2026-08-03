package unitreview

import (
	"context"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
)

func TestHypothesisHookAnchorsOutputToUnitScope(t *testing.T) {
	var hypotheses []Hypothesis
	hook := &HypothesisHook{OnResolved: func(h Hypothesis) { hypotheses = append(hypotheses, h) }}
	checkpoint, handled := hook.HandleTool(context.Background(), harness.ToolRequest{
		Scope: session.Scope{
			ID: "chain", Kind: "unit", Type: "callchain", Paths: []string{"a.go", "b.go"},
		},
		Tool: SubmitHypotheses,
		Args: map[string]any{
			"hypotheses": []any{map[string]any{
				"path":               "outside.go",
				"content":            "returns stale data",
				"existing_code":      "return cached",
				"trigger":            "the cache entry expires",
				"impact":             "the caller sees stale data",
				"change_attribution": "the diff removed the expiry check",
				"evidence":           []any{"a.go:10"},
				"uncertainty":        "",
				"category":           "bug",
				"severity":           "high",
			}},
		},
	})
	if !handled || !checkpoint.Completed || checkpoint.Data != HypothesesSubmitted {
		t.Fatalf("unexpected hook result: handled=%v checkpoint=%+v", handled, checkpoint)
	}
	if len(hypotheses) != 1 || hypotheses[0].Path != "a.go" || hypotheses[0].OriginUnit != "chain" {
		t.Fatalf("hypothesis must stay inside its Unit scope: %+v", hypotheses)
	}
}

func TestHypothesisHookAcceptsNoHypotheses(t *testing.T) {
	hook := &HypothesisHook{}
	checkpoint, handled := hook.HandleTool(context.Background(), harness.ToolRequest{
		Scope: session.Scope{ID: "unit-1", Kind: "unit", Paths: []string{"a.go"}},
		Tool:  SubmitHypotheses,
		Args:  map[string]any{"hypotheses": []any{}},
	})
	if !handled || !checkpoint.Completed || checkpoint.Data != HypothesesSubmitted {
		t.Fatalf("empty submission was not accepted: handled=%v checkpoint=%+v", handled, checkpoint)
	}
}
