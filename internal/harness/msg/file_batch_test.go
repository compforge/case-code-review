package msg

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func TestFileBatchKeepsOnePairingAndTypedMembers(t *testing.T) {
	content := tool.EncodeFileReadResults([]string{
		fileResult("a.go", 20, 1, 10, "1|a\n"),
		"Error: file missing.go not found",
		fileResult("b.go", 30, 11, 20, "11|b\n"),
	})
	message := FromLLM(LLMToolResult{
		Tool: FileReadToolName, ToolCallID: "batch-1", Content: content,
	})
	batch, ok := message.(*FileBatch)
	if !ok || len(batch.Files()) != 2 {
		t.Fatalf("batch promotion = %#v", message)
	}
	if batch.Files()[0].Path != "a.go" || batch.Files()[1].Path != "b.go" {
		t.Fatalf("file order = %#v", batch.Files())
	}
	wire := batch.ToLLM(CompactionNone)
	if wire.ToolCallID != "batch-1" || !strings.Contains(wire.ExtractText(), "missing.go") {
		t.Fatalf("batch lowering lost pairing/error: %+v", wire)
	}
}

func TestDedupFilesHandlesBatchMembers(t *testing.T) {
	older := mkFile(t, "a.go", 20, 1, 10)
	content := tool.EncodeFileReadResults([]string{
		fileResult("a.go", 20, 1, 20, "1|new\n"),
		fileResult("b.go", 20, 1, 20, "1|other\n"),
	})
	batch := FromLLM(LLMToolResult{
		Tool: FileReadToolName, ToolCallID: "batch-1", Content: content,
	}).(*FileBatch)

	if got := DedupFiles([]Msg{older, batch}); got != 1 || !older.Stubbed() {
		t.Fatalf("dedup batch members = %d, older stubbed=%t", got, older.Stubbed())
	}
}
