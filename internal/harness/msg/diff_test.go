package msg

import (
	"strings"
	"testing"

	"github.com/compforge/agentgo"
)

func TestDiffSourceCoveragePreservesChangesAndRebuilds(t *testing.T) {
	body := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old value\n+new value\n"
	diff := NewDiff([]string{"a.go"}, body).ConfigureReviewSource([]SourceArtifact{{Path: "a.go", Snapshot: SnapshotCurrent, Lines: []SourceArtifactLine{{Number: 1, Text: "new value"}}}})
	file := NewFile("a.go", 1, 2, 2, "File: a.go (Total lines: 2)\n1|new value\n2|keep this\n")
	input := []agentgo.AgentMessage{diff, file}
	view := TransformSource(agentgo.TransformContext{Messages: input})
	if view[0].TextContent() != body || strings.Contains(view[1].TextContent(), "1|new value") || !strings.Contains(view[1].TextContent(), "2|keep this") {
		t.Fatal("incorrect diff coverage", view[1].TextContent())
	}
	if view[1].Raw().TextContent() != file.TextContent() {
		t.Fatal("lost raw source")
	}
	compacted, _ := diff.Compact(0)
	restored := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{compacted, view[1]}})
	if !strings.Contains(restored[1].TextContent(), "1|new value") {
		t.Fatal("compacted diff left dangling reference")
	}
	file.Snapshot = SnapshotBaseline
	baseline := TransformSource(agentgo.TransformContext{Messages: []agentgo.AgentMessage{diff, file}})
	if !strings.Contains(baseline[1].TextContent(), "1|new value") {
		t.Fatal("conflated before/after")
	}
}
