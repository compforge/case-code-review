package msg

import (
	"fmt"
	"strings"
	"testing"
)

func fileResult(path string, total, start, end int, body string) string {
	return fmt.Sprintf("File: %s (Total lines: %d)\nIS_TRUNCATED: false\nLINE_RANGE: %d-%d\n%s",
		path, total, start, end, body)
}

func mkFile(t *testing.T, path string, total, start, end int) *File {
	t.Helper()
	result := fileResult(path, total, start, end, "1|code\n")
	f := &File{}
	if !f.FromLLM(LLMToolResult{Tool: FileReadToolName, ToolCallID: "c1", Content: result}) {
		t.Fatalf("expected promotion for %s", path)
	}
	return f
}

func TestFileFromLLM(t *testing.T) {
	f := mkFile(t, "pkg/a.go", 120, 10, 40)
	if f.Path != "pkg/a.go" || f.Start != 10 || f.End != 40 || f.Total != 120 {
		t.Fatalf("parsed identity off: %+v", f)
	}
	// Other tools and malformed results stay Raw.
	if (&File{}).FromLLM(LLMToolResult{Tool: "search_code", ToolCallID: "c1", Content: "hits"}) {
		t.Fatal("non-read_files must not promote")
	}
	if (&File{}).FromLLM(LLMToolResult{Tool: FileReadToolName, ToolCallID: "c1", Content: `Error: file "x" not found`}) {
		t.Fatal("error result must not promote")
	}
	// Lowering an un-stubbed File is the original wire message.
	if got := f.Lower(); got.ToolCallID != "c1" || !strings.Contains(got.ExtractText(), "1|code") {
		t.Fatalf("lowered wire off: %+v", got)
	}
}

func TestFileFromBaselineResultKeepsSnapshotIdentity(t *testing.T) {
	result := "Baseline ref: abc123\n" + fileResult("pkg/a.go", 120, 10, 40, "10|old code\n")
	baseline := &File{}
	ok := baseline.FromLLM(LLMToolResult{Tool: FileReadBaseToolName, ToolCallID: "base-1", Content: result})
	if !ok || baseline.Snapshot != SnapshotBaseline || baseline.Ref != "abc123" {
		t.Fatalf("baseline promotion off: %+v ok=%t", baseline, ok)
	}
	current := mkFile(t, "pkg/a.go", 120, 10, 40)
	if baseline.Covers(current) || current.Covers(baseline) {
		t.Fatal("baseline and current snapshots must never deduplicate each other")
	}
	baselineWire := baseline.ToLLM(CompactionNone)
	path, start, end, total, ok := VisibleFileRange(baselineWire.ExtractText())
	if !ok || path != "pkg/a.go" || start != 10 || end != 40 || total != 120 {
		t.Fatalf("baseline visible range = %q %d-%d/%d ok=%t", path, start, end, total, ok)
	}
}

func TestVisibleFileRange(t *testing.T) {
	path, start, end, total, ok := VisibleFileRange("File: pkg/a.go (Total lines: 12)\n1|package a\n")
	if !ok || path != "pkg/a.go" || start != 1 || end != 12 || total != 12 {
		t.Fatalf("whole preload range = %q %d-%d/%d ok=%t", path, start, end, total, ok)
	}
	path, start, end, total, ok = VisibleFileRange(fileResult("pkg/a.go", 120, 10, 40, "10|code\n"))
	if !ok || path != "pkg/a.go" || start != 10 || end != 40 || total != 120 {
		t.Fatalf("ranged file result = %q %d-%d/%d ok=%t", path, start, end, total, ok)
	}
	if _, _, _, _, ok := VisibleFileRange("File: pkg/a.go lines 1-12 — elided"); ok {
		t.Fatal("stubbed file must not count as visible coverage")
	}
}

func TestFileCompactionPreservesRangeAndPairing(t *testing.T) {
	f := mkFile(t, "pkg/a.go", 120, 10, 40)
	f.ConfigurePresentation("code under review", "File: pkg/a.go (Total lines: 120)\nLINE_RANGE: 10-40\n10|condensed\n")

	full := f.ToLLM(CompactionNone)
	if full.ToolCallID != "c1" || !strings.Contains(full.ExtractText(), "CONTEXT: code under review") {
		t.Fatalf("full projection lost pairing or label: %+v", full)
	}
	path, start, end, total, ok := VisibleFileRange(full.ExtractText())
	if !ok || path != "pkg/a.go" || start != 10 || end != 40 || total != 120 {
		t.Fatalf("labeled range = %q %d-%d/%d ok=%t", path, start, end, total, ok)
	}
	if got := VisibleFileLabel(full.ExtractText()); got != "code under review" {
		t.Fatalf("label = %q", got)
	}

	condensed := f.ToLLM(CompactionCondensed)
	if !strings.Contains(condensed.ExtractText(), "10|condensed") || condensed.ToolCallID != "c1" {
		t.Fatalf("condensed projection off: %+v", condensed)
	}
	reference := f.ToLLM(CompactionReference)
	if reference.ToolCallID != "c1" || !strings.Contains(reference.ExtractText(), "compacted to a reference") {
		t.Fatalf("reference projection off: %+v", reference)
	}
	if _, _, _, _, ok := VisibleFileRange(reference.ExtractText()); ok {
		t.Fatal("reference-only file must not count as visible content")
	}
}

func TestFileStubKeepsPairing(t *testing.T) {
	f := mkFile(t, "pkg/a.go", 120, 10, 40)
	f.Stub(StubSuperseded)
	got := f.Lower()
	if got.Role != "tool" || got.ToolCallID != "c1" {
		t.Fatalf("stub must keep the tool_result pairing: %+v", got)
	}
	if !strings.Contains(got.ExtractText(), "superseded") || strings.Contains(got.ExtractText(), "1|code") {
		t.Fatalf("stub must elide content: %q", got.ExtractText())
	}

	// Eviction has its own pointer text (how to get the content back), and the
	// first stub reason wins.
	e := mkFile(t, "pkg/b.go", 10, 1, 10)
	e.Stub(StubEvicted)
	ew := e.Lower()
	if txt := ew.ExtractText(); !strings.Contains(txt, "context budget") || !strings.Contains(txt, "read_files") {
		t.Fatalf("evicted stub text off: %q", txt)
	}
	f.Stub(StubEvicted)
	fw := f.Lower()
	if !strings.Contains(fw.ExtractText(), "superseded") {
		t.Fatal("first stub reason must win")
	}
}

func TestDedupFiles(t *testing.T) {
	old := mkFile(t, "pkg/a.go", 120, 10, 40)
	other := mkFile(t, "pkg/b.go", 50, 1, 50)
	partial := mkFile(t, "pkg/a.go", 120, 5, 20) // overlaps but not covered by newer
	newer := mkFile(t, "pkg/a.go", 120, 10, 60)  // covers old, not partial

	msgs := []Msg{Text("user", "task"), old, other, partial, Text("assistant", "…"), newer}
	if n := DedupFiles(msgs); n != 1 {
		t.Fatalf("stubbed = %d, want 1", n)
	}
	if !old.Stubbed() {
		t.Fatal("covered earlier read must be stubbed")
	}
	if other.Stubbed() || partial.Stubbed() || newer.Stubbed() {
		t.Fatal("uncovered / different-path / newest reads must be kept")
	}
	// Idempotent: a second pass finds nothing new.
	if n := DedupFiles(msgs); n != 0 {
		t.Fatalf("second pass stubbed %d, want 0", n)
	}
}
