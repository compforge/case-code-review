package unitreview

import (
	"context"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
)

func TestHypothesisHookAnchorsOutputToUnitScope(t *testing.T) {
	collector := NewCollector()
	hook := &HypothesisHook{Collector: collector}
	checkpoint, handled := hook.HandleTool(context.Background(), harness.ToolRequest{
		Scope: session.Scope{
			ID: "chain", Kind: "unit", Type: "callchain", Paths: []string{"a.go", "b.go"},
		},
		Tool: ReportHypothesis,
		Args: map[string]any{
			"path": "outside.go",
			"hypotheses": []any{map[string]any{
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
	if !handled || checkpoint.Data != HypothesisSubmitted {
		t.Fatalf("unexpected hook result: handled=%v checkpoint=%+v", handled, checkpoint)
	}
	hypotheses := collector.Hypotheses()
	if len(hypotheses) != 1 || hypotheses[0].Path != "a.go" || hypotheses[0].OriginUnit != "chain" {
		t.Fatalf("hypothesis must stay inside its Unit scope: %+v", hypotheses)
	}
}
