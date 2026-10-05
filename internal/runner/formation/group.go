package formation

import (
	"sort"
	"strings"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

const DefaultGroupDiffTokens = 8000
const maxGroupFiles = 5
const maxGroupFragments = 8
const maxGroupLines int64 = 300

type candidate struct {
	from, to int
	evidence unit.GroupingEvidence
}

// groupFragments groups only supplied targets. The graph remains the source of
// relationships; this derived partition is CCR policy, never new code facts.
func groupFragments(fragments []unit.Fragment, after, before *language.Analyzer, related bool, tokenLimit int) []unit.Unit {
	sort.Slice(fragments, func(i, j int) bool { return unit.FragmentID(fragments[i]) < unit.FragmentID(fragments[j]) })
	var edges []candidate
	if related {
		edges = append(edges, graphCandidates(fragments, after, false)...)
		edges = append(edges, graphCandidates(fragments, before, true)...)
	}
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
	paths := map[string]bool{}
	sizes := make([]int, len(fragments))
	groupCount := len(fragments)
	for i := range fragments {
		groups[i] = []int{i}
		owner[i] = i
		paths[fragments[i].Path] = true
		sizes[i] = len(fragments[i].Diff)
	}
	fits := func(ids []int) bool {
		if len(ids) > maxGroupFragments {
			return false
		}
		paths := map[string]bool{}
		var fs []unit.Fragment
		var lines int64
		for _, i := range ids {
			f := fragments[i]
			paths[f.Path] = true
			lines += f.Insertions + f.Deletions
			fs = append(fs, f)
		}
		return len(paths) <= maxGroupFiles && lines <= maxGroupLines && llm.CountTokens((unit.Unit{Fragments: fs}).Diff()) <= tokenLimit
	}
	merge := func(a, b int, bounded bool) bool {
		a, b = owner[a], owner[b]
		if a == b {
			return true
		}
		ids := append(append([]int(nil), groups[a]...), groups[b]...)
		if bounded && !fits(ids) {
			return false
		}
		groups[a] = ids
		groups[b] = nil
		sizes[a] += sizes[b]
		groupCount--
		for _, i := range ids {
			owner[i] = a
		}
		return true
	}
	for _, e := range edges {
		merge(e.from, e.to, true)
	}
	// Without graph ownership, retain a bounded per-file fallback. Knowing only
	// that two functions share a file is not a semantic grouping decision.
	for i := range fragments {
		for j := 0; j < i; j++ {
			a, b := fragments[i], fragments[j]
			if a.Path == b.Path && (!related || (len(a.Before)+len(a.After) == 0 && len(b.Before)+len(b.After) == 0)) {
				merge(i, j, true)
			}
		}
	}
	// Graph-backed groups come first. When they would create more loops than
	// changed files, coalesce groups sharing a file, smallest combined patch
	// first. A shared path always exists while groupCount > distinct paths.
	// This preserves existing call groups and every edit, including imports and
	// residuals, without inventing a source relation between unrelated symbols.
	// The count cap takes precedence over size budgets; oversize Units remain
	// explicit below and the runner reports them incomplete rather than clean.
	for groupCount > len(paths) {
		left, right := smallestFilePair(fragments, owner, sizes)
		merge(left, right, false)
	}
	var out []unit.Unit
	for _, ids := range groups {
		if len(ids) == 0 {
			continue
		}
		var fs []unit.Fragment
		for _, i := range ids {
			fs = append(fs, fragments[i])
		}
		u := unit.NewRelatedUnit(fs)
		for _, e := range edges {
			if owner[e.from] == owner[ids[0]] && owner[e.to] == owner[ids[0]] {
				u.Grouping = append(u.Grouping, e.evidence)
			} else if owner[e.from] == owner[ids[0]] || owner[e.to] == owner[ids[0]] {
				u.Boundaries = append(u.Boundaries, e.evidence)
			}
		}
		u.DiffTokens = llm.CountTokens(u.Diff())
		u.BudgetExceeded = u.DiffTokens > tokenLimit
		if len(u.Grouping) == 0 && len(fs) > 1 {
			u.Formed = unit.FormedCoalesce
		}
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Only the two smallest distinct groups per file can be the next merge. Scan
// memberships once per merge instead of comparing every fragment pair; large
// files can contain hundreds of independent declarations or import bindings.
func smallestFilePair(fs []unit.Fragment, owner, sizes []int) (int, int) {
	type pair struct{ first, second int }
	byFile := map[string]pair{}
	less := func(a, b int) bool { return b < 0 || sizes[a] < sizes[b] || sizes[a] == sizes[b] && a < b }
	for i, f := range fs {
		p, ok := byFile[f.Path]
		if !ok {
			p = pair{-1, -1}
		}
		id := owner[i]
		if id == p.first || id == p.second {
			continue
		}
		if less(id, p.first) {
			p = pair{id, p.first}
		} else if less(id, p.second) {
			p.second = id
		}
		byFile[f.Path] = p
	}
	left, right, size := -1, -1, 0
	for _, p := range byFile {
		if p.second < 0 {
			continue
		}
		a, b := min(p.first, p.second), max(p.first, p.second)
		combined := sizes[a] + sizes[b]
		if left < 0 || combined < size || combined == size && (a < left || a == left && b < right) {
			left, right, size = a, b, combined
		}
	}
	return left, right
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
