package formation

import (
	"context"
	cg "github.com/compforge/codegraph"
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
	"sort"
)

const DefaultGroupDiffTokens = 8000
const maxGroupFiles = 5

// CCR owns token accounting, review state, clues and Session output; repocli
// owns Fragment/Unit grouping and graph-backed merge decisions.
func formRepositoryUnits(config Config, tokenLimit int) ([]unit.Unit, error) {
	ctx := config.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, finish := session.Begin(ctx, "unit.grouping")
	var before, after *cg.Graph
	if config.Before != nil {
		before = config.Before.Repository().Graph
	}
	if config.Analyzer != nil {
		after = config.Analyzer.Repository().Graph
	}
	input := config.Diff.WithGraphs(before, after)
	if config.Changes != nil {
		input.Changes = config.Changes
	}
	paths := map[string]bool{}
	for _, ch := range input.Changes {
		if config.Exclude == nil || !config.Exclude(ch) {
			paths[ch.Path()] = true
		}
	}
	// Count-driven coalescing should not force a target below the selected-file count.
	// Strong source relations may still produce fewer Units.
	maxUnits := max(len(paths), config.MaxUnits)
	result, err := repocli.FormUnits(ctx, input, repocli.UnitOptions{Exclude: config.Exclude, FileOnly: !config.CallChain, MaxUnits: maxUnits, MaxFiles: maxGroupFiles, MaxChangedLines: 300, MaxDiffSize: tokenLimit, DiffSize: llm.CountTokens})
	if err != nil {
		finish(err)
		return nil, err
	}
	fragments, err := unit.BindFragments(ctx, result.Fragments, result.Changes, config.RepoDir, config.Analyzer, config.Before)
	if err != nil {
		finish(err)
		return nil, err
	}
	byID := map[string]unit.Fragment{}
	for _, f := range fragments {
		byID[unit.FragmentID(f)] = f
	}
	report := GroupingReport{InitialUnits: len(paths), MaxUnits: maxUnits, FinalUnits: len(result.Units), LimitExceeded: result.LimitExceeded}
	for _, s := range result.Steps {
		step := GroupingStep{Strategy: s.Strategy, InputUnits: s.InputUnits, OutputUnits: s.OutputUnits, BudgetBlocked: s.BudgetBlocked}
		if s.Strategy == "namespace" {
			for _, m := range result.Merges {
				ref := language.NamespaceRef{Snapshot: m.Snapshot, NodeID: m.Namespace}
				for _, g := range []*cg.Graph{before, after} {
					if g != nil && g.Snapshot() == m.Snapshot {
						if n, ok := g.Node(m.Namespace); ok {
							ref.Name = n.Name
						}
					}
				}
				step.Merges = append(step.Merges, GroupingMerge{Namespace: ref, Targets: m.FragmentIDs, Paths: m.Proofs})
			}
		}
		report.Steps = append(report.Steps, step)
		if config.OnStep != nil {
			config.OnStep(ctx, step)
		}
	}
	convert := func(es []repocli.FragmentRelation) []unit.GroupingEvidence {
		var out []unit.GroupingEvidence
		for _, e := range es {
			out = append(out, unit.GroupingEvidence{Before: e.Before, FromFragment: e.FromFragment, ToFragment: e.ToFragment, Link: language.Connection(e.Link)})
		}
		return out
	}
	var out []unit.Unit
	for _, formed := range result.Units {
		// Review policy applies after grouping so imports can still join code Units.
		if onlyImportElements(formed.Counts) {
			continue
		}
		var fs []unit.Fragment
		for _, id := range formed.FragmentIDs {
			fs = append(fs, byID[id])
		}
		u := unit.NewRelatedUnit(fs)
		u.Repo = formed
		u.ID = formed.ID
		u.Grouping = convert(formed.Relations)
		u.Boundaries = convert(formed.Boundaries)
		u.DiffTokens = formed.DiffSize
		u.BudgetExceeded = formed.BudgetExceeded || u.DiffTokens > tokenLimit
		if len(u.Paths()) == 1 {
			u.Scope, u.Formed = unit.ScopeFile, unit.FormedFile
		}
		out = append(out, u)
	}
	if len(out) != len(result.Units) {
		step := GroupingStep{Strategy: "skip_import_only", InputUnits: len(result.Units), OutputUnits: len(out)}
		report.Steps = append(report.Steps, step)
		if config.OnStep != nil {
			config.OnStep(ctx, step)
		}
	}
	report.FinalUnits = len(out)
	report.LimitExceeded = maxUnits > 0 && len(out) > maxUnits
	if config.OnGrouped != nil {
		config.OnGrouped(report)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	finish(nil)
	return out, nil
}

// Empty counts also qualify: review eligibility uses import count == total count.
// WARNING: this is a scope policy, not proof of unchanged behavior. Side-effect
// imports, initialization registration and dependency replacements can be skipped.
func onlyImportElements(counts repocli.ElementCounts) bool {
	imports, total := 0, 0
	for _, side := range []map[repocli.ElementKind]int{counts.Before, counts.After} {
		for kind, count := range side {
			total += count
			if kind == repocli.ElementImport {
				imports += count
			}
		}
	}
	return imports == total
}
