package runner

import (
	"context"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// countingFinder records how many times it is asked to find clues.
type countingFinder struct{ n *int }

func (f countingFinder) Find(unit.Unit) []unit.Clue { *f.n++; return nil }

func TestSplitUnits_ContextIsNotDisabledByChangeCount(t *testing.T) {
	// Coalesced targets receive context for their final shared Unit.
	var under int
	au := &Runner{
		changes:       []change.Change{goDiff("p.go", 3)},
		costlyFinders: []unit.ClueFinder{countingFinder{&under}},
	}
	if _, err := au.splitUnits(context.Background()); err != nil {
		t.Fatal(err)
	}
	if under != 1 {
		t.Errorf("small change: finder should run for each final Unit, got %d", under)
	}

	// A larger change set retains the same per-Unit context behavior.
	var over int
	ao := &Runner{
		changes:       []change.Change{goDiff("p.go", 12)},
		costlyFinders: []unit.ClueFinder{countingFinder{&over}},
	}
	if _, err := ao.splitUnits(context.Background()); err != nil {
		t.Fatal(err)
	}
	if over != 1 {
		t.Errorf("large change: finder should still run for each final Unit, got %d calls", over)
	}
}

func TestRenderClues(t *testing.T) {
	specCases, rules, seeAlso := renderClues([]unit.Clue{
		{Kind: unit.ClueSpec, Text: "F spec\n  - case"},
		{Kind: unit.ClueRule, Text: "watch DB"},
		{Kind: unit.ClueRule, Text: "hot path"},
		{Kind: unit.ClueLink, Text: "docs/x.md (doc)"},
		{Kind: unit.ClueProject, Relation: unit.RelProject, Ref: "pyproject.toml", Text: "changed manifest"},
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
	project := renderProjectContext([]unit.Clue{
		{Kind: unit.ClueProject, Relation: unit.RelProject, Ref: "pyproject.toml", Text: "changed manifest"},
	})
	if project != "- (same Component project context `pyproject.toml`) changed manifest" {
		t.Errorf("project context: %q", project)
	}

	if s, r, l := renderClues(nil); s != "" || r != "" || l != "" {
		t.Errorf("empty clues should render empty: %q / %q / %q", s, r, l)
	}
}
