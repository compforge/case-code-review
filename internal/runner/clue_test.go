package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// countingFinder records how many times it is asked to find clues.
type countingFinder struct{ n *int }

func (f countingFinder) Find(unit.Unit) []unit.Clue { *f.n++; return nil }

func TestSplitUnits_CostlyFindersGatedByBudget(t *testing.T) {
	// Below the watermark: the single-file change is one file Unit, so the costly
	// finder runs once against that Unit's complete symbol set.
	var under int
	au := &Runner{
		splitter:      unit.AutoSplitter{},
		changes:       []change.Change{goDiff("p.go", 3)},
		costlyFinders: []unit.ClueFinder{countingFinder{&under}},
	}
	if _, err := au.splitUnits(); err != nil {
		t.Fatal(err)
	}
	if under != 1 {
		t.Errorf("under watermark: costly finder should run once for the file Unit, got %d", under)
	}

	// Above the watermark: units will coalesce → costly finder skipped entirely.
	var over int
	ao := &Runner{
		splitter:      unit.AutoSplitter{},
		changes:       []change.Change{goDiff("p.go", defaultUnitWatermark+2)},
		costlyFinders: []unit.ClueFinder{countingFinder{&over}},
	}
	if _, err := ao.splitUnits(); err != nil {
		t.Fatal(err)
	}
	if over != 0 {
		t.Errorf("over watermark: costly finder should be skipped, got %d calls", over)
	}
}

func TestRenderClues(t *testing.T) {
	specCases, rules, seeAlso, prior := renderClues([]unit.Clue{
		{Kind: unit.ClueSpec, Text: "F spec\n  - case"},
		{Kind: unit.ClueRule, Text: "watch DB"},
		{Kind: unit.ClueRule, Text: "hot path"},
		{Kind: unit.ClueLink, Text: "docs/x.md (doc)"},
		{Kind: unit.ClueHistory, Text: "prior: missing nil check"},
	})
	if specCases != "F spec\n  - case" {
		t.Errorf("specCases: %q", specCases)
	}
	if rules != "- watch DB\n- hot path" {
		t.Errorf("rules: %q", rules)
	}
	if seeAlso != "- docs/x.md (doc)" {
		t.Errorf("seeAlso: %q", seeAlso)
	}
	if prior != "prior: missing nil check" {
		t.Errorf("prior: %q", prior)
	}

	if s, r, l, h := renderClues(nil); s != "" || r != "" || l != "" || h != "" {
		t.Errorf("empty clues should render empty: %q / %q / %q / %q", s, r, l, h)
	}
}
