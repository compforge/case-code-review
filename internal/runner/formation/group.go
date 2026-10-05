package formation

import (
	"context"
	"sort"
	"strings"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

const DefaultGroupDiffTokens = 8000
const maxGroupFiles = 5
const maxGroupLines int64 = 300

type candidate struct {
	from, to int
	evidence unit.GroupingEvidence
}

// RelationGrouper reorganizes file groups around changed source dependencies.
// Disabled keeps the initial file groups, preserving the existing feature gate.
type RelationGrouper struct{ Disabled bool }

func (RelationGrouper) Name() string { return "relations" }
func (g RelationGrouper) Group(ctx context.Context, in GroupingInput) (GroupingResult, error) {
	if err := ctx.Err(); err != nil {
		return GroupingResult{}, err
	}
	if g.Disabled {
		return GroupingResult{Groups: in.Groups}, nil
	}
	fragments := flattenGroups(in.Groups)
	edges := append(graphCandidates(fragments, in.After, false), graphCandidates(fragments, in.Before, true)...)
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.evidence.Link.Touched != b.evidence.Link.Touched {
			return a.evidence.Link.Touched
		}
		if a.evidence.Link.Confidence != b.evidence.Link.Confidence {
			return a.evidence.Link.Confidence.AtLeast(b.evidence.Link.Confidence)
		}
		if a.from != b.from {
			return a.from < b.from
		}
		if a.to != b.to {
			return a.to < b.to
		}
		x, y := a.evidence.Link, b.evidence.Link
		return strings.Join([]string{string(x.Kind), x.Snapshot, x.Source, x.Target}, "\x00") < strings.Join([]string{string(y.Kind), y.Snapshot, y.Source, y.Target}, "\x00")
	})
	groups := make([][]int, len(fragments))
	owner := make([]int, len(fragments))
	for i := range fragments {
		groups[i] = []int{i}
		owner[i] = i
	}
	merge := func(a, b int, bounded bool) bool {
		a, b = owner[a], owner[b]
		if a == b {
			return true
		}
		ids := append(append([]int(nil), groups[a]...), groups[b]...)
		if bounded && !fitsGroup(selectFragments(fragments, ids), in.DiffTokens) {
			return false
		}
		groups[a] = ids
		groups[b] = nil
		for _, i := range ids {
			owner[i] = a
		}
		return true
	}
	for _, e := range edges {
		if err := ctx.Err(); err != nil {
			return GroupingResult{}, err
		}
		merge(e.from, e.to, true)
	}
	// Extract graph-backed cross-file groups first. Everything left in each
	// file shares its default review scope: unrelated declarations, imports and
	// residual edits do not each get a separate loop. In particular A->B must
	// not pull an unrelated C from A's file into the cross-file group.
	byFile := map[string]int{}
	for root, ids := range groups {
		if len(ids) == 0 {
			continue
		}
		path := fragments[ids[0]].Path
		sameFile := true
		for _, i := range ids[1:] {
			if fragments[i].Path != path {
				sameFile = false
				break
			}
		}
		if !sameFile {
			continue
		}
		if previous, ok := byFile[path]; ok {
			merge(previous, root, false)
		} else {
			byFile[path] = root
		}
	}

	var out []Group
	for _, ids := range groups {
		if len(ids) > 0 {
			out = append(out, selectFragments(fragments, ids))
		}
	}
	var evidence []unit.GroupingEvidence
	for _, edge := range edges {
		evidence = append(evidence, edge.evidence)
	}
	return GroupingResult{Groups: out, Relations: evidence}, nil
}

func selectFragments(fs []unit.Fragment, ids []int) Group {
	out := make(Group, 0, len(ids))
	for _, id := range ids {
		out = append(out, fs[id])
	}
	return out
}

// Materialize once, after all regrouping. Evidence always describes the final
// partition, including relationships reunited by a later namespace pass.
func materialize(groups []Group, edges []unit.GroupingEvidence, tokenLimit int) []unit.Unit {
	owner := map[string]int{}
	for i, group := range groups {
		for _, f := range group {
			owner[unit.FragmentID(f)] = i
		}
	}
	var out []unit.Unit
	for i, group := range groups {
		u := unit.NewRelatedUnit(group)
		for _, e := range edges {
			a, b := owner[e.FromFragment], owner[e.ToFragment]
			if a == i && b == i {
				u.Grouping = append(u.Grouping, e)
			} else if a == i || b == i {
				u.Boundaries = append(u.Boundaries, e)
			}
		}
		u.DiffTokens = llm.CountTokens(u.Diff())
		u.BudgetExceeded = u.DiffTokens > tokenLimit
		if len(u.Paths()) == 1 {
			u.Scope, u.Formed = unit.ScopeFile, unit.FormedFile
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func graphCandidates(fs []unit.Fragment, a *language.Analyzer, before bool) []candidate {
	if a == nil {
		return nil
	}
	index := a.Repository()
	if index.Graph == nil {
		return nil
	}
	byNode := map[string][]int{}
	for i, f := range fs {
		anchors := f.After
		if before {
			anchors = f.Before
		}
		for _, anchor := range anchors {
			byNode[anchor.NodeID] = append(byNode[anchor.NodeID], i)
		}
	}
	var out []candidate
	seen := map[candidate]bool{}
	for i, f := range fs {
		anchors, path := f.After, f.Path
		if before {
			anchors, path = f.Before, f.OldPath
		}
		for _, link := range index.Connections(anchors, path, f.ChangedSpans(before)) {
			for _, j := range byNode[link.Target] {
				if i == j {
					continue
				}
				e := candidate{from: min(i, j), to: max(i, j), evidence: unit.GroupingEvidence{Before: before, Link: link, FromFragment: unit.FragmentID(f), ToFragment: unit.FragmentID(fs[j])}}
				if !seen[e] {
					out = append(out, e)
					seen[e] = true
				}
			}
		}
	}
	return out
}
