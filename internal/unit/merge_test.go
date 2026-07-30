package unit

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/diff"
	"github.com/qiankunli/case-code-review/internal/language"
)

// fileWith builds a FileFragments for path with n function fragments (each id
// path::f), each covering one line — enough for the merger's counting.
func fileWith(path string, n int) FileFragments {
	d := diff.Diff{NewPath: path, Diff: "@@ -1,1 +1,1 @@\n-a\n+b\n"}
	var fs []Fragment
	for range n {
		id := language.SymbolID(path, "", "f")
		fs = append(fs, Fragment{Path: path, Symbols: []string{id}, Diff: d.Diff})
	}
	return FileFragments{Diff: d, Fragments: fs}
}

func TestWatermarkMerger_UnderWatermarkKeepsFragments(t *testing.T) {
	m := WatermarkMerger{Watermark: 10}
	review := m.Merge([]FileFragments{fileWith("a.go", 2), fileWith("b.go", 1)})
	if len(review) != 3 {
		t.Fatalf("under watermark: want 3 review units (fragments kept), got %d", len(review))
	}
	for _, u := range review {
		if u.Scope != ScopeFunc {
			t.Errorf("want func scope, got %v", u.Scope)
		}
	}
}

func TestWatermarkMerger_OverWatermarkCoalescesMultiFragmentFiles(t *testing.T) {
	// 9 single-fragment files + 1 two-fragment file = 11 > 10: only the
	// multi-fragment file coalesces -> 9 func review units + 1 file review unit.
	var files []FileFragments
	for i := range 9 {
		files = append(files, fileWith("s.go", 1))
		files[i].Diff.NewPath = string(rune('a'+i)) + ".go"
		files[i].Fragments[0].Path = files[i].Diff.NewPath
	}
	files = append(files, fileWith("multi.go", 2))

	review := WatermarkMerger{Watermark: 10}.Merge(files)
	funcs, fileUnits := 0, 0
	for _, u := range review {
		switch u.Scope {
		case ScopeFunc:
			funcs++
		case ScopeFile:
			fileUnits++
		}
	}
	if funcs != 9 || fileUnits != 1 {
		t.Fatalf("want 9 func + 1 file review unit, got %d func + %d file", funcs, fileUnits)
	}
}
