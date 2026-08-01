package hypothesisreview

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
