package formation

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type staticFinder []unit.Clue

func (f staticFinder) Find(unit.Unit) []unit.Clue { return f }

func TestFormDeduplicatesCluesAfterScopeIsFinal(t *testing.T) {
	clues := staticFinder{
		{Kind: unit.ClueSpec, Relation: unit.RelSelf, Text: "contract"},
		{Kind: unit.ClueSpec, Relation: unit.RelSelf, Text: "contract"},
		{Kind: unit.ClueSpec, Relation: unit.RelOwner, Text: "contract"},
		{Kind: unit.ClueRule, Relation: unit.RelUsed, Text: "per-request"},
	}
	units, _, err := Form(Config{
		Changes:  []change.Change{{NewPath: "a.go", Diff: "@@ -1 +1 @@\n-old\n+new"}},
		Splitter: unit.FileSplitter{},
		Finders:  []unit.ClueFinder{clues},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || len(units[0].Clues) != 3 {
		t.Fatalf("unexpected formed units: %+v", units)
	}
}
