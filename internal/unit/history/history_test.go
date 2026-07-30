package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestLoad(t *testing.T) {
	if idx, err := Load(""); idx != nil || err != nil {
		t.Errorf("empty path -> nil,nil; got %v,%v", idx, err)
	}

	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	if err := os.WriteFile(p, []byte(`{"a.go::F":[{"msg":"x","sha":"abc"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx["a.go::F"]) != 1 || idx["a.go::F"][0].Msg != "x" {
		t.Errorf("load: %+v", idx)
	}

	empty := filepath.Join(dir, "e.json")
	os.WriteFile(empty, []byte("   "), 0o644)
	if i, err := Load(empty); i != nil || err != nil {
		t.Errorf("empty file -> nil,nil; got %v,%v", i, err)
	}

	bad := filepath.Join(dir, "b.json")
	os.WriteFile(bad, []byte("{not json"), 0o644)
	if _, err := Load(bad); err == nil {
		t.Error("malformed JSON should error")
	}
}

func TestFinder(t *testing.T) {
	idx := Index{"a.go::F": {{Msg: "missing nil check", Sha: "abc1234"}}}

	u := unit.UnitOf(unit.Fragment{Path: "a.go", Symbols: []string{"a.go::F"}})
	clues := Finder{Index: idx}.Find(u)
	if len(clues) != 1 || clues[0].Kind != unit.ClueHistory {
		t.Fatalf("want 1 history clue, got %+v", clues)
	}
	// framed as an adjudication task, carrying the finding + sha.
	for _, want := range []string{"previous review", "missing nil check", "abc1234"} {
		if !strings.Contains(clues[0].Text, want) {
			t.Errorf("clue text missing %q: %q", want, clues[0].Text)
		}
	}

	// a unit with no prior findings -> nil
	other := unit.UnitOf(unit.Fragment{Path: "a.go", Symbols: []string{"a.go::G"}})
	if got := (Finder{Index: idx}).Find(other); got != nil {
		t.Errorf("no findings -> nil, got %+v", got)
	}
	// nil index -> nil
	if got := (Finder{}).Find(u); got != nil {
		t.Errorf("nil index -> nil, got %+v", got)
	}
}
