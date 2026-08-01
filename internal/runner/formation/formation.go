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
	case costly && config.CallChain:
		adjacency := codegraph.CallAdjacency(
			config.RepoDir, config.GitRunner, config.TypedGraph, funcIDsOf(files),
		)
		chains, residual := clusterByCallChain(files, adjacency)
		units = append(units, chains...)
		units = append(units, merger.Merge(residual)...)
	default:
		units = merger.Merge(files)
	}

	for i := range units {
		units[i].Clues = findClues(units[i], config.Finders, config.CostlyFinders, costly)
	}
	return units, costly, nil
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
