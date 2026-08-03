// Package formation turns filtered changes into the stable Units consumed by
// Unit Review. It owns target splitting, semantic/cost grouping and Clue
// attachment; it does not execute an agent loop.
package formation

import (
	"fmt"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"github.com/qiankunli/case-code-review/internal/unit/codegraph"
	"github.com/qiankunli/go-stdx/slicesx"
)

// DefaultWatermark bounds the number of function-grained review Units before
// Formation coarsens the remaining work to file scope.
const DefaultWatermark = 10

// Config supplies the rules and knowledge sources needed to form Units. The
// zero value keeps semantic call-chain grouping off and uses the default cost
// governor.
type Config struct {
	RepoDir       string
	Changes       []change.Change
	Splitter      unit.Splitter
	Merger        unit.Merger
	Finders       []unit.ClueFinder
	CostlyFinders []unit.ClueFinder
	GitRunner     *gitcmd.Runner
	TypedGraph    *codegraph.TypedGraph
	CallChain     bool
	Watermark     int
}

// Form returns the Units and whether the diff stayed below the expensive
// context watermark. Clues are gathered only after each Unit's scope is final.
func Form(config Config) ([]unit.Unit, bool, error) {
	watermark := config.Watermark
	if watermark <= 0 {
		watermark = DefaultWatermark
	}
	splitter := config.Splitter
	if splitter == nil {
		splitter = unit.AutoSplitter{RepoDir: config.RepoDir}
	}

	var files []unit.FileFragments
	total := 0
	for i := range config.Changes {
		if config.Changes[i].IsDeleted {
			continue
		}
		fragments, err := splitter.Split(config.Changes[i])
		if err != nil {
			return nil, false, fmt.Errorf("split units for %s: %w", config.Changes[i].NewPath, err)
		}
		files = append(files, unit.FileFragments{Diff: config.Changes[i], Fragments: fragments})
		total += len(fragments)
	}

	merger := config.Merger
	if merger == nil {
		merger = unit.WatermarkMerger{Watermark: watermark}
	}
	costly := total <= watermark
	var units []unit.Unit
	switch {
	case len(files) == 1:
		units = append(units, unit.CoalesceFile(files[0].Diff, files[0].Fragments))
	case config.CallChain && total <= watermark*2:
		adjacency := codegraph.CallAdjacency(
			config.RepoDir, config.GitRunner, config.TypedGraph, funcIDsOf(files),
		)
		units = mergeCallChains(files, adjacency, merger)
	default:
		units = merger.Merge(files)
	}

	for i := range units {
		units[i].Clues = findClues(units[i], config.Finders, config.CostlyFinders, costly)
	}
	return units, costly, nil
}

func mergeCallChains(
	files []unit.FileFragments,
	adjacency map[string][]string,
	merger unit.Merger,
) []unit.Unit {
	chains, _ := clusterByCallChain(files, adjacency)
	if len(chains) == 0 {
		return merger.Merge(files)
	}

	// Keep semantic chains independently: one chain that duplicates both of its
	// files must not force an unrelated, useful chain back to file scope.
	selected := make([]unit.Unit, 0, len(chains))
	for _, chain := range chains {
		candidate := append(append([]unit.Unit(nil), selected...), chain)
		if len(candidate)+len(residualAfterChains(files, candidate)) <= len(files) {
			selected = candidate
		}
	}
	if len(selected) == 0 {
		return coalesceFiles(files)
	}

	units := append([]unit.Unit(nil), selected...)
	residual := residualAfterChains(files, selected)
	for _, file := range residual {
		if len(file.Fragments) > 0 {
			units = append(units, unit.CoalesceFragments(file.Fragments))
		}
	}
	return units
}

func residualAfterChains(files []unit.FileFragments, chains []unit.Unit) []unit.FileFragments {
	selected := make(map[string]struct{})
	for _, chain := range chains {
		for _, fragment := range chain.Fragments {
			if len(fragment.Symbols) == 1 {
				selected[fragment.Symbols[0]] = struct{}{}
			}
		}
	}

	residual := make([]unit.FileFragments, 0, len(files))
	for _, file := range files {
		fragments := make([]unit.Fragment, 0, len(file.Fragments))
		for _, fragment := range file.Fragments {
			if len(fragment.Symbols) == 1 {
				if _, ok := selected[fragment.Symbols[0]]; ok {
					continue
				}
			}
			fragments = append(fragments, fragment)
		}
		if len(fragments) > 0 {
			residual = append(residual, unit.FileFragments{Diff: file.Diff, Fragments: fragments})
		}
	}
	return residual
}

func coalesceFiles(files []unit.FileFragments) []unit.Unit {
	units := make([]unit.Unit, 0, len(files))
	for _, file := range files {
		if len(file.Fragments) > 0 {
			units = append(units, unit.CoalesceFile(file.Diff, file.Fragments))
		}
	}
	return units
}

func findClues(
	reviewUnit unit.Unit,
	finders []unit.ClueFinder,
	costlyFinders []unit.ClueFinder,
	includeCostly bool,
) []unit.Clue {
	var clues []unit.Clue
	for _, finder := range finders {
		clues = append(clues, finder.Find(reviewUnit)...)
	}
	if includeCostly {
		for _, finder := range costlyFinders {
			clues = append(clues, finder.Find(reviewUnit)...)
		}
	}
	return slicesx.UniqBy(clues, func(clue unit.Clue) string {
		return string(clue.Relation) + "\x00" + string(clue.Kind) + "\x00" + clue.Text
	})
}
