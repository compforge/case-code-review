package unit

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func TestFileSplitter_OneFragmentPerFile(t *testing.T) {
	d := change.Change{
		NewPath:    "internal/foo/bar.go",
		Diff:       "@@ -1,2 +1,3 @@\n+added line\n",
		Insertions: 1,
		Deletions:  0,
	}

	frags, err := FileSplitter{}.Split(d)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if len(frags) != 1 {
		t.Fatalf("expected 1 fragment, got %d", len(frags))
	}

	f := frags[0]
	u := UnitOf(f) // whole-file fragment -> file-scope Unit (ID = path)
	if u.Scope != ScopeFile {
		t.Errorf("scope: got %q, want %q", u.Scope, ScopeFile)
	}
	if u.ID != d.NewPath || f.Path != d.NewPath {
		t.Errorf("identity: ID=%q Path=%q, want both %q", u.ID, f.Path, d.NewPath)
	}
	if len(f.Symbols) != 0 {
		t.Errorf("file-scope fragment should cover no functions, got Symbols %v", f.Symbols)
	}
	if f.Diff != d.Diff {
		t.Errorf("Diff: got %q, want whole-file diff %q", f.Diff, d.Diff)
	}
	if f.Insertions != d.Insertions || f.Deletions != d.Deletions {
		t.Errorf("counts: got +%d/-%d, want +%d/-%d (file's own)",
			f.Insertions, f.Deletions, d.Insertions, d.Deletions)
	}
}
