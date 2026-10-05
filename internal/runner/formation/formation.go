// Package formation turns filtered changes into the stable Units consumed by
// Unit Review. It owns target splitting, semantic/cost grouping and Clue
// attachment; it does not execute an agent loop.
package formation

import (
	"fmt"
	"strings"

	"github.com/compforge/go-stdx/slicesx"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// Config supplies the rules and knowledge sources needed to form Units. The
// zero value keeps relation grouping off and uses default cross-file merge budgets.
type Config struct {
	RepoDir         string
	Changes         []change.Change
	Splitter        unit.Splitter
	Finders         []unit.ClueFinder
	CostlyFinders   []unit.ClueFinder
	Analyzer        *language.Analyzer
	Before          *language.Analyzer
	GroupDiffTokens int
	CallChain       bool
}

// Form extracts graph-related changed targets into cross-file Units and groups
// remaining edits by file. Clues are gathered after the final scope is known.
//
// +spec=`Each target edit belongs to exactly one Unit; changed cross-file dependencies are grouped before file-local remainders, within a file-count allowance`
func Form(config Config) ([]unit.Unit, error) {
	if config.Analyzer == nil && config.RepoDir != "" {
		config.Analyzer = language.NewAnalyzer(config.RepoDir)
	}
	tokenLimit := config.GroupDiffTokens
	if tokenLimit <= 0 {
		tokenLimit = DefaultGroupDiffTokens
	}
	splitter := config.Splitter
	if splitter == nil {
		splitter = unit.AutoSplitter{RepoDir: config.RepoDir, Analyzer: config.Analyzer, Before: config.Before}
	}
	var fragments []unit.Fragment
	for _, d := range config.Changes {
		fs, err := splitter.Split(d)
		if err != nil {
			return nil, fmt.Errorf("split units for %s: %w", d.Path(), err)
		}
		// Context lines may repeat, edits may not. Fragment granularity describes
		// source ownership; it must not force another review loop for every node.
		if err := validateEdits(d, fs); err != nil {
			return nil, err
		}
		fragments = append(fragments, fs...)
	}
	units := groupFragments(fragments, config.Analyzer, config.Before, config.CallChain, tokenLimit)
	for i := range units {
		units[i].Clues = findClues(units[i], config.Finders, config.CostlyFinders)
		units[i].Clues = append(units[i].Clues, boundaryClues(units[i].Boundaries)...)
	}
	return units, nil
}

// validateEdits compares coordinate/content multisets rather than reported line
// counts: synthetic inputs and metadata-only changes can have zero churn fields.
func validateEdits(d change.Change, fs []unit.Fragment) error {
	want := editCounts(d.Diff)
	got := map[string]int{}
	for _, f := range fs {
		for key, n := range editCounts(f.Diff) {
			got[key] += n
		}
	}
	if len(want) != len(got) {
		return fmt.Errorf("fragment coverage mismatch for %s", d.Path())
	}
	for k, n := range want {
		if got[k] != n {
			return fmt.Errorf("fragment coverage mismatch for %s at %s", d.Path(), k)
		}
	}
	return nil
}
func editCounts(diff string) map[string]int {
	out := map[string]int{}
	for _, h := range change.ParseHunks(diff) {
		old, new := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			if l.Type == change.HunkAdded {
				out[fmt.Sprintf("+%d:%s", new, l.Content)]++
				new++
			} else if l.Type == change.HunkDeleted {
				out[fmt.Sprintf("-%d:%s", old, l.Content)]++
				old++
			} else {
				old++
				new++
			}
		}
	}
	return out
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
