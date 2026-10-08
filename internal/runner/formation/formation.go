// Package formation turns captured changes and caller policy into Units consumed by
// Unit Review. It delegates splitting/grouping to repocli and owns review Clue
// attachment; it does not execute an agent loop.
package formation

import (
	"context"
	"fmt"
	"strings"

	"github.com/compforge/go-stdx/slicesx"
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// Config supplies the rules and knowledge sources needed to form Units. The
// zero value keeps relation grouping off and uses default cross-file merge budgets.
type Config struct {
	Context   context.Context
	MaxUnits  int
	OnStep    func(context.Context, GroupingStep)
	OnGrouped func(GroupingReport)
	RepoDir   string
	// ExcludeChange must be pure: it is also consulted when counting selected files for grouping budgets.
	ExcludeChange   func(change.Change) bool
	Changes         []change.Change
	Diff            repocli.DiffReport
	Finders         []unit.ClueFinder
	CostlyFinders   []unit.ClueFinder
	Analyzer        *language.Analyzer
	Before          *language.Analyzer
	GroupDiffTokens int
	CallChain       bool
}

// Form delegates repository grouping to repocli before creating review Units.
// Clues are gathered only after the final scope is known.
//
// +spec=`Each edit has one Fragment identity; review Units can share evidenced imports and preserve explicit grouping limits`
func Form(config Config) ([]unit.Unit, error) {
	if config.Analyzer == nil && config.RepoDir != "" {
		config.Analyzer = language.NewAnalyzer(config.RepoDir)
	}
	tokenLimit := config.GroupDiffTokens
	if tokenLimit <= 0 {
		tokenLimit = DefaultGroupDiffTokens
	}
	units, err := formRepositoryUnits(config, tokenLimit)
	if err != nil {
		return nil, err
	}
	for i := range units {
		units[i].Clues = findClues(units[i], config.Finders, config.CostlyFinders)
		units[i].Clues = append(units[i].Clues, boundaryClues(units[i].Boundaries)...)
	}
	return units, nil
}

func findClues(
	reviewUnit unit.Unit,
	finders []unit.ClueFinder,
	costlyFinders []unit.ClueFinder,
) []unit.Clue {
	var clues []unit.Clue
	for _, finder := range finders {
		clues = append(clues, finder.Find(reviewUnit)...)
	}
	for _, finder := range costlyFinders {
		clues = append(clues, finder.Find(reviewUnit)...)
	}
	return slicesx.UniqBy(clues, func(clue unit.Clue) string {
		return strings.Join([]string{string(clue.Relation), string(clue.Kind), clue.Ref, clue.Snapshot, clue.Text}, "\x00")
	})
}

// Full cut evidence remains in the formation trace. Prompt context is bounded
// independently: several source occurrences can describe the same file dependency.
func boundaryClues(boundaries []unit.GroupingEvidence) []unit.Clue {
	const limit = 8
	seen := map[string]bool{}
	var out []unit.Clue
	for _, boundary := range boundaries {
		link := boundary.Link
		key := strings.Join([]string{link.SourcePath, link.TargetPath, string(link.Kind), link.Snapshot}, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(out) < limit {
			out = append(out, unit.Clue{Kind: unit.ClueProject, Relation: unit.RelUsed, Ref: link.TargetPath, Text: fmt.Sprintf("Related change across Unit budget boundary: %s -> %s (%s, snapshot %s). Inspect the related diff when checking this dependency.", link.SourcePath, link.TargetPath, link.Kind, link.Snapshot)})
		}
	}
	if len(seen) > limit {
		out = append(out, unit.Clue{Kind: unit.ClueProject, Relation: unit.RelUsed, Text: fmt.Sprintf("%d additional cross-Unit dependencies omitted from initial context; formation trace retains the full graph evidence.", len(seen)-limit)})
	}
	return out
}
