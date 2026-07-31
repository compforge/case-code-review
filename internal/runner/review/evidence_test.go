package review

import (
	"context"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func TestReviewHandlerReceiptsComeFromExecutedReadTools(t *testing.T) {
	registry := tool.NewRegistry()
	registry.Register(tool.NewFileReadDiff(tool.NewDiffMap(map[string]string{
		"a.go": "@@ -1 +1 @@\n-old\n+new",
	})))
	ledger := &EvidenceLedger{}
	handler := &ReviewHandler{Evidence: ledger, Tools: registry}
	checkpoint, handled := handler.HandleTool(context.Background(), harness.ToolRequest{
		Tool: tool.FileReadDiff,
		Call: llm.ToolCall{ID: "call-diff"},
		Args: map[string]any{"path_array": []any{"a.go", "missing.go"}},
	})
	if !handled || checkpoint.Data == "" {
		t.Fatalf("unexpected tool result: handled=%v checkpoint=%+v", handled, checkpoint)
	}
	receipts := ledger.Receipts()
	if len(receipts) != 1 || receipts[0].Kind != "diff" || receipts[0].Ref != "a.go" {
		t.Fatalf("unexpected receipts: %+v", receipts)
	}
}

func TestPassesTrialRejectsModelEvidenceWithoutReceipt(t *testing.T) {
	hypothesis := Hypothesis{Path: "a.go"}
	assessment := Assessment{
		Support: Supported, Attribution: Caused,
		Value: Actionable, Novelty: Novel, Evidence: []string{"a.go:10"},
	}
	if PassesTrial(hypothesis, assessment) {
		t.Fatal("model-authored evidence must not replace a CCR receipt")
	}
	assessment.EvidenceReceipts = []EvidenceReceipt{{Kind: "diff", Ref: "a.go"}}
	if !PassesTrial(hypothesis, assessment) {
		t.Fatal("complete assessment with a matching diff receipt should pass")
	}
}
