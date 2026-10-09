package hypothesisreview

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestDependencySourceRetainsIdentityAndCannotMintRepositoryReceipt(t *testing.T) {
	source := tool.GoDependencyResult{Status: "read", Path: "example.org/dep/dep.go", Ref: "example.org/dep@v1#sha256=abc", Start: 1, End: 2, Total: 2, Content: "versioned source"}
	bytes, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	reviewUnit := unit.Unit{}
	unitreview.AttachResult(&reviewUnit, tool.ReadGoDependency.Name(), map[string]any{}, string(bytes))
	snapshot := reviewUnit.Review()
	if len(snapshot.FileSnapshots) != 1 || snapshot.FileSnapshots[0].Kind != unit.DependencySnapshot {
		t.Fatalf("snapshots=%+v", snapshot.FileSnapshots)
	}
	receipts := UnitReceipts(reviewUnit)
	if len(receipts) != 1 || receipts[0].Kind != "dependency" || receipts[0].Ref != source.Ref {
		t.Fatalf("receipts=%+v", receipts)
	}
	messages := reviewContextMessages(ReviewInput{Unit: reviewUnit})
	if len(messages) != 1 {
		t.Fatalf("messages=%d", len(messages))
	}
	file, ok := messages[0].(*msg.File)
	if !ok || file.Snapshot != msg.SnapshotDependency || file.ToolName() != tool.ReadGoDependency.Name() {
		t.Fatalf("dependency identity lost: %#v", messages[0])
	}
	items := file.ContextItems()
	if len(items) != 1 || items[0].Kind != "dependency_file" || items[0].Identity != source.Ref || items[0].Ref != source.Ref {
		t.Fatalf("dependency projection=%+v", items)
	}
	compacted, _ := file.Compact(0)
	if !strings.Contains(compacted.TextContent(), source.Ref) {
		t.Fatal("compaction lost dependency version identity")
	}
	request := harness.ToolRequest{Tool: tool.ReadGoDependency, Call: llm.ToolCall{ID: "read-1"}}
	if got := receiptsFor(request, string(bytes)); len(got) != 1 || got[0].Kind != "dependency" {
		t.Fatalf("read receipts=%+v", got)
	}
	source.Status = "unavailable"
	bytes, _ = json.Marshal(source)
	if got := receiptsFor(request, string(bytes)); len(got) != 0 {
		t.Fatalf("unavailable source minted receipts=%+v", got)
	}
}

func TestDependencyScopeComesFromCurrentHypothesis(t *testing.T) {
	tools := tool.NewRegistry()
	tools.Register(tool.NewBuiltin(tool.ReadGoDependency, func(_ context.Context, args map[string]any) (string, error) {
		if args["_reviewed_path"] != "nested/source.go" {
			t.Fatalf("scope=%+v", args)
		}
		return `{"status":"unavailable"}`, nil
	}))
	args := map[string]any{"import_path": "example.org/dep", "_reviewed_path": "model-chosen/path"}
	handler := ReviewHandler{Tools: tools, Hypothesis: unit.Hypothesis{Path: "nested/source.go"}}
	_, handled := handler.HandleTool(t.Context(), harness.ToolRequest{Tool: tool.ReadGoDependency, Args: args})
	if !handled || args["_reviewed_path"] != "model-chosen/path" {
		t.Fatal("handler changed recorded model arguments")
	}
}
