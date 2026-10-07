package tool

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMergeSearchBatchDeduplicatesOverlapsAndRetainsOutcomes(t *testing.T) {
	out := MergeCodeSearchResults([]string{
		"File: a.go\nMatch lines: 1\n2|beta\nContext:\nLINE_RANGE: 1-3\n1|alpha\n2|beta\n3|gamma",
		"File: a.go\nMatch lines: 1\n3|gamma\nContext:\nLINE_RANGE: 2-4\n2|beta\n3|gamma\n4|delta",
		`Search outcome: {"status":"scope_empty","query_mode":"literal","searched_files":0}`,
		"Error: invalid regex",
	})
	parts, ok := DecodeCodeSearchResults(out)
	if !ok || len(parts) != 4 {
		t.Fatalf("lost query envelopes: %s", out)
	}
	if !strings.Contains(parts[0], "Hit line: 2") || !strings.Contains(parts[1], "Hit line: 3") {
		t.Fatal(out)
	}
	if outcome, ok := ParseCodeSearchOutcome(parts[2]); !ok || outcome.Status != CodeSearchScopeEmpty {
		t.Fatal(parts[2])
	}
	if parts[3] != "Error: invalid regex" {
		t.Fatal(parts[3])
	}
	_, shared := SplitCodeSearchSource(out)
	if strings.Count(shared, "File: a.go") != 1 {
		t.Fatal(shared)
	}
	for _, line := range []string{"1|alpha", "2|beta", "3|gamma", "4|delta"} {
		if strings.Count(shared, line) != 1 {
			t.Fatalf("line repeated/missing: %s", shared)
		}
	}
}

func TestMergeSearchBatchBoundsLongLinesAndManyFiles(t *testing.T) {
	var result strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&result, "File: %d.map\nMatch lines: 1\n1|%s\n", i, strings.Repeat("你好", 1000))
	}
	out := MergeCodeSearchResults([]string{result.String(), "Error: later query failed"})
	if len(out) > MaxResultBytes || !utf8.ValidString(out) || !strings.Contains(out, "Output truncated") {
		t.Fatalf("invalid bounded output: %d bytes", len(out))
	}
	parts, ok := DecodeCodeSearchResults(out)
	if !ok || len(parts) != 2 || parts[1] != "Error: later query failed" {
		t.Fatal("later query lost")
	}
	if len(CodeSearchSourceRanges(out)) != 0 {
		t.Fatal("invented complete source")
	}
}

func TestMergeSearchBatchOnlyReceiptsCompleteSymbolSource(t *testing.T) {
	for _, long := range []bool{false, true} {
		body := "func Alpha() {}"
		if long {
			body = strings.Repeat("a", 2000)
		}
		result := "File: a.go\nMatch lines: 1\n2|" + body + "\n" +
			`Symbol source: {"path":"a.go","start_line":1,"end_line":2,"total_lines":2}` + "\n1|package a\n2|" + body
		out := MergeCodeSearchResults([]string{result, result})
		ranges := CodeSearchSourceRanges(out)
		if (!long && len(ranges) != 1) || (long && len(ranges) != 0) {
			t.Fatalf("long=%t ranges=%v", long, ranges)
		}
		if strings.Count(out, "1|package a") != 1 {
			t.Fatal("symbol source repeated")
		}
	}
}

func TestLimitResultUTF8AndBoundary(t *testing.T) {
	for _, size := range []int{0, 1, 4, 128, 1024, MaxResultBytes} {
		out := LimitResult(strings.Repeat("中", 20000), size)
		if len(out) > size || !utf8.ValidString(out) {
			t.Fatalf("size=%d got %d bytes", size, len(out))
		}
	}
	exact := strings.Repeat("x", MaxResultBytes)
	if LimitResult(exact, MaxResultBytes) != exact {
		t.Fatal("changed output at boundary")
	}
}
