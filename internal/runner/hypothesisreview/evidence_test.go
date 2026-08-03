package hypothesisreview

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
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
		Args: map[string]any{"paths": []any{"a.go", "missing.go"}},
	})
	if !handled || checkpoint.Data == "" {
		t.Fatalf("unexpected tool result: handled=%v checkpoint=%+v", handled, checkpoint)
	}
	receipts := ledger.Receipts()
	if len(receipts) != 1 || receipts[0].Kind != "diff" || receipts[0].Ref != "a.go" {
		t.Fatalf("unexpected receipts: %+v", receipts)
	}
}

func TestReviewHandlerReceiptsEachSuccessfulBatchMember(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.NewFileRead(&tool.FileReader{RepoDir: dir, Mode: tool.ModeWorkspace}))
	ledger := &EvidenceLedger{}
	reviewUnit := unit.UnitOf(unit.Fragment{Path: "target.go"})
	handler := &ReviewHandler{Evidence: ledger, Tools: registry, Unit: &reviewUnit}
	checkpoint, handled := handler.HandleTool(context.Background(), harness.ToolRequest{
		Tool: tool.FileRead,
		Call: llm.ToolCall{ID: "call-source"},
		Args: map[string]any{"reads": []any{
			map[string]any{"file_path": "a.go"},
			map[string]any{"file_path": "missing.go"},
		}},
	})
	if !handled || checkpoint.Data == "" {
		t.Fatalf("unexpected tool result: handled=%v checkpoint=%+v", handled, checkpoint)
	}
	receipts := ledger.Receipts()
	if len(receipts) != 1 || receipts[0].Kind != "source" || receipts[0].Ref != "a.go" {
		t.Fatalf("unexpected receipts: %+v", receipts)
	}
	snapshots := reviewUnit.Review().FileSnapshots
	if len(snapshots) != 1 || snapshots[0].Kind != unit.CurrentSnapshot || snapshots[0].Path != "a.go" {
		t.Fatalf("Review 2 read was not retained on Unit: %+v", snapshots)
	}
}

func TestReviewHandlerReceiptsEachSuccessfulSearch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tool.NewRegistry()
	registry.Register(tool.NewCodeSearch(&tool.FileReader{RepoDir: dir, Mode: tool.ModeWorkspace}))
	ledger := &EvidenceLedger{}
	handler := &ReviewHandler{Evidence: ledger, Tools: registry}
	checkpoint, handled := handler.HandleTool(context.Background(), harness.ToolRequest{
		Tool: tool.CodeSearch,
		Call: llm.ToolCall{ID: "call-search"},
		Args: map[string]any{"searches": []any{
			map[string]any{"query": "package a", "syntax": "literal"},
			map[string]any{"query": "", "syntax": "literal"},
			map[string]any{"query": "missing", "syntax": "literal"},
		}},
	})
	if !handled || checkpoint.Data == "" {
		t.Fatalf("unexpected tool result: handled=%v checkpoint=%+v", handled, checkpoint)
	}
	receipts := ledger.Receipts()
	if len(receipts) != 2 || receipts[0].Ref != "missing" || receipts[1].Ref != "package a" {
		t.Fatalf("unexpected search receipts: %+v", receipts)
	}
}

func TestEvidenceLedgerDeduplicatesRetainedReceipts(t *testing.T) {
	receipt := EvidenceReceipt{ToolCallID: "unit:diff", Kind: "diff", Ref: "a.go"}
	ledger := &EvidenceLedger{receipts: []EvidenceReceipt{receipt, receipt}}
	if got := ledger.Receipts(); len(got) != 1 || got[0] != receipt {
		t.Fatalf("deduplicated receipts = %+v, want %+v", got, receipt)
	}
}
