package formation

import (
	"context"
	"fmt"
	"maps"
	"sort"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/go-stdx/timeline"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// Group is a partition of review targets, before context and review state exist.
type Group []unit.Fragment

type GroupingInput struct {
	Groups        []Group
	After, Before *language.Analyzer
	MaxUnits      int
	DiffTokens    int
}

type GroupingMerge struct {
	Namespace language.NamespaceRef `json:"namespace"`
	Targets   []string              `json:"targets"`
	Paths     []cg.Path             `json:"paths"`
}

type GroupingResult struct {
	Relations        []unit.GroupingEvidence
	Groups           []Group
	Merges           []GroupingMerge
	BudgetBlocked    int
	MissingNamespace int
}

// Grouper may repartition targets, but must retain every Fragment exactly once.
// It owns the grouping rule; GroupChain owns ordering, stopping and validation.
type Grouper interface {
	Name() string
	Group(context.Context, GroupingInput) (GroupingResult, error)
}

type GroupingStep struct {
	Strategy         string          `json:"strategy"`
	InputUnits       int             `json:"input_units"`
	OutputUnits      int             `json:"output_units"`
	Merges           []GroupingMerge `json:"merges,omitempty"`
	BudgetBlocked    int             `json:"budget_blocked,omitempty"`
	MissingNamespace int             `json:"missing_namespace,omitempty"`
}

type GroupingReport struct {
	InitialUnits  int            `json:"initial_units"`
	MaxUnits      int            `json:"max_units"`
	FinalUnits    int            `json:"final_units"`
	LimitExceeded bool           `json:"limit_exceeded"`
	Steps         []GroupingStep `json:"steps"`
}

// GroupChain always runs its first strategy, even below the count target:
// related changes deserve a shared review independently of scheduling pressure.
// Later strategies run only while the count exceeds MaxUnits. An unattainable
// target remains visible; it never authorizes dropping edits or forcing a merge.
type GroupChain struct {
	Groupers []Grouper
	OnStep   func(context.Context, GroupingStep)
}

func (c GroupChain) Group(ctx context.Context, in GroupingInput) (GroupingResult, GroupingReport, error) {
	if hasEmptyGroup(in.Groups) {
		return GroupingResult{}, GroupingReport{}, fmt.Errorf("group chain received an empty group")
	}
	canonicalize(in.Groups)
	var output GroupingResult
	report := GroupingReport{InitialUnits: len(in.Groups), MaxUnits: in.MaxUnits, FinalUnits: len(in.Groups)}
	for i, grouper := range c.Groupers {
		if i > 0 && len(in.Groups) <= in.MaxUnits {
			break
		}
		if err := ctx.Err(); err != nil {
			return GroupingResult{}, report, err
		}
		stepCtx, finish := session.Begin(ctx, "unit.grouping."+grouper.Name(), timeline.Attribute{Key: "input_units", Value: len(in.Groups)}, timeline.Attribute{Key: "max_units", Value: in.MaxUnits})
		// Capture identities before the strategy can mutate its input slices.
		expected := fragmentCounts(in.Groups)
		result, err := grouper.Group(stepCtx, in)
		if err == nil && (!maps.Equal(expected, fragmentCounts(result.Groups)) || hasEmptyGroup(result.Groups)) {
			err = fmt.Errorf("grouper %s changed target coverage", grouper.Name())
		}
		if err != nil {
			finish(err)
			return GroupingResult{}, report, err
		}
		canonicalize(result.Groups)
		step := GroupingStep{Strategy: grouper.Name(), InputUnits: len(in.Groups), OutputUnits: len(result.Groups), Merges: result.Merges, BudgetBlocked: result.BudgetBlocked, MissingNamespace: result.MissingNamespace}
		if c.OnStep != nil {
			c.OnStep(stepCtx, step)
		}
		finish(nil)
		report.Steps = append(report.Steps, step)
		output.Relations = append(output.Relations, result.Relations...)
		output.Merges = append(output.Merges, result.Merges...)
		output.BudgetBlocked += result.BudgetBlocked
		output.MissingNamespace += result.MissingNamespace
		in.Groups = result.Groups
	}
	report.FinalUnits = len(in.Groups)
	report.LimitExceeded = report.FinalUnits > report.MaxUnits
	output.Groups = in.Groups
	return output, report, nil
}

func fileGroups(fs []unit.Fragment) []Group {
	byPath := map[string]Group{}
	for _, f := range fs {
		byPath[f.Path] = append(byPath[f.Path], f)
	}
	groups := make([]Group, 0, len(byPath))
	for _, group := range byPath {
		groups = append(groups, group)
	}
	canonicalize(groups)
	return groups
}

func canonicalize(groups []Group) {
	for _, group := range groups {
		sort.Slice(group, func(i, j int) bool { return unit.FragmentID(group[i]) < unit.FragmentID(group[j]) })
	}
	sort.Slice(groups, func(i, j int) bool { return unit.FragmentID(groups[i][0]) < unit.FragmentID(groups[j][0]) })
}

func flattenGroups(groups []Group) []unit.Fragment {
	var fs []unit.Fragment
	for _, group := range groups {
		fs = append(fs, group...)
	}
	sort.Slice(fs, func(i, j int) bool { return unit.FragmentID(fs[i]) < unit.FragmentID(fs[j]) })
	return fs
}

func fragmentCounts(groups []Group) map[string]int {
	counts := map[string]int{}
	for _, group := range groups {
		for _, f := range group {
			counts[unit.FragmentID(f)]++
		}
	}
	return counts
}

func hasEmptyGroup(groups []Group) bool {
	for _, group := range groups {
		if len(group) == 0 {
			return true
		}
	}
	return false
}

func fitsGroup(group Group, tokenLimit int) bool {
	paths := map[string]bool{}
	var lines int64
	for _, f := range group {
		paths[f.Path] = true
		lines += f.Insertions + f.Deletions
	}
	return len(paths) <= maxGroupFiles && lines <= maxGroupLines && llm.CountTokens((unit.Unit{Fragments: group}).Diff()) <= tokenLimit
}
