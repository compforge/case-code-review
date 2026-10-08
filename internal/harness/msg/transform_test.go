package msg

import (
	"fmt"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
)

func sourceFile(path string, start, end int) *File {
	var body strings.Builder
	fmt.Fprintf(&body, "File: %s (Total lines: 40)\nIS_TRUNCATED: true\nLINE_RANGE: %d-%d\n", path, start, end)
	for n := start; n <= end; n++ {
		fmt.Fprintf(&body, "%d|line %d\n", n, n)
	}
	return NewFile(path, start, end, 40, body.String())
}

func TestTransformSourceOverlapsAndRebuildsAfterCompaction(t *testing.T) {
	a, b := sourceFile("a.go", 1, 20), sourceFile("a.go", 10, 30)
	input := []agentgo.AgentMessage{a, b}
	view := TransformSource(agentgo.TransformContext{Messages: input})
	if !strings.Contains(view[1].TextContent(), "Lines 10-20 already shown") || strings.Contains(view[1].TextContent(), "10|line 10\n") || !strings.Contains(view[1].TextContent(), "21|line 21\n") {
		t.Fatal(view[1].TextContent())
	}
	if view[1].Raw().TextContent() != b.TextContent() {
		t.Fatal("raw lost")
	}
	again := TransformSource(agentgo.TransformContext{Messages: view})
	if again[1].TextContent() != view[1].TextContent() {
		t.Fatal("non-idempotent transform")
	}
	reduced, _ := a.Compact(0)
	rebuilt := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{reduced, view[1]}})
	if !strings.Contains(rebuilt[1].TextContent(), "10|line 10\n") {
		t.Fatal("reference remained after source compacted")
	}
}

func TestTransformSourceKeepsVersionsErrorsAndCallPairing(t *testing.T) {
	base, current := sourceFile("a.go", 1, 2), sourceFile("a.go", 1, 2)
	base.Snapshot = SnapshotBaseline
	base.Ref = "old"
	if view := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{base, current}}); view[1].TextContent() != current.TextContent() {
		t.Fatal("mixed snapshots")
	}
	batch := FromLLM(LLMToolResult{Tool: FileReadToolName, ToolCallID: "read-2", Content: tool.EncodeFileReadResults([]string{current.Content, "Error: absent"})})
	view := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{current, batch}})
	m, _ := view[1].ToMessage()
	if m.Metadata["tool_call_id"] != "read-2" || !strings.Contains(view[1].TextContent(), "Error: absent") || !strings.Contains(view[1].TextContent(), "already shown") {
		t.Fatalf("lost pairing/outcome: %+v", m)
	}
	changed := *current
	changed.Content = strings.ReplaceAll(changed.Content, "line 1", "different")
	if view := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{current, &changed}}); !strings.Contains(view[1].TextContent(), "1|different") {
		t.Fatal("changed content hidden")
	}
}

func TestTransformSourceSharesSearchAndReadEvidence(t *testing.T) {
	file := sourceFile("a.go", 1, 3)
	search := FromLLM(LLMToolResult{Tool: CodeSearchToolName, ToolCallID: "search", Arguments: map[string]any{"searches": []any{map[string]any{"query": "line"}}}, Content: tool.MergeCodeSearchResults([]string{"File: a.go\nMatch lines: 1\n2|line 2\nContext:\nLINE_RANGE: 1-3\n1|line 1\n2|line 2\n3|line 3"})})
	for _, input := range [][]agentgo.AgentMessage{{file, search}, {search, file}} {
		view := TransformSource(agentgo.TransformContext{Messages: input})
		if !strings.Contains(view[1].TextContent(), "already shown") || strings.Contains(view[1].TextContent(), "2|line 2\n") {
			t.Fatal(view[1].TextContent())
		}
	}
}

func TestClippedSourceDoesNotCoverLaterFullLine(t *testing.T) {
	content := "File: a.go\nMatch lines: 1\n1|" + strings.Repeat("x", 2000)
	search := FromLLM(LLMToolResult{Tool: CodeSearchToolName, ToolCallID: "s", Arguments: map[string]any{"searches": []any{map[string]any{"query": "x"}}}, Content: tool.MergeCodeSearchResults([]string{content})})
	full := NewFile("a.go", 1, 1, 1, "1|"+strings.Repeat("x", 2000)+"\n")
	view := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{search, full}})
	if view[1].TextContent() != full.TextContent() {
		t.Fatal("clipped source claimed full coverage")
	}
}
