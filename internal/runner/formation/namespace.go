package formation

import (
	"context"
	"sort"

	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// NamespaceGrouper coalesces whole groups under a common CodeGraph organization.
// CCR chooses the merge; language hierarchy and each ownership proof stay upstream.
type NamespaceGrouper struct{}

func (NamespaceGrouper) Name() string { return "namespace" }
func (NamespaceGrouper) Group(ctx context.Context, in GroupingInput) (GroupingResult, error) {
	groups := append([]Group(nil), in.Groups...)
	scopes := make([]map[language.NamespaceRef]cg.NamespaceMatch, len(groups))
	result := GroupingResult{}
	type documentKey struct {
		index *language.RepositoryIndex
		path  string
	}
	documents := map[documentKey]map[language.NamespaceRef]cg.NamespaceMatch{}
	documentNamespaces := func(index *language.RepositoryIndex, path string) (map[language.NamespaceRef]cg.NamespaceMatch, error) {
		key := documentKey{index, path}
		if refs, ok := documents[key]; ok {
			return refs, nil
		}
		refs, err := index.CommonNamespaces(ctx, nil, path)
		if err != nil {
			return nil, err
		}
		documents[key] = refs
		return refs, nil
	}
	for i, group := range groups {
		for j, f := range group {
			if err := ctx.Err(); err != nil {
				return GroupingResult{}, err
			}
			a, anchors, path := in.After, f.After, f.Path
			if len(anchors) == 0 && len(f.Before) > 0 {
				a, anchors, path = in.Before, f.Before, f.OldPath
			}
			var refs map[language.NamespaceRef]cg.NamespaceMatch
			if a != nil {
				index := a.Repository()
				var err error
				if len(anchors) > 0 {
					refs, err = index.CommonNamespaces(ctx, anchors, path)
				}
				if err != nil {
					return GroupingResult{}, err
				}
				// File grouping is the policy fallback for unbound targets. The Document's
				// organization must still be proven by CodeGraph; errors are not absence.
				if len(refs) == 0 {
					refs, err = documentNamespaces(index, path)
				}
				if err != nil {
					return GroupingResult{}, err
				}
			}
			if len(refs) == 0 && len(f.After) == 0 && len(f.Before) == 0 && in.Before != nil {
				var err error
				refs, err = documentNamespaces(in.Before.Repository(), f.OldPath)
				if err != nil {
					return GroupingResult{}, err
				}
			}
			if j == 0 {
				scopes[i] = refs
			} else {
				scopes[i] = commonNamespaces(scopes[i], refs)
			}
		}
		if len(scopes[i]) == 0 {
			result.MissingNamespace++
		}
	}
	for len(groups) > in.MaxUnits {
		left, right, bestDepth, bestSize := -1, -1, 0, 0
		var namespace language.NamespaceRef
		blocked := 0
		for i := range groups {
			if err := ctx.Err(); err != nil {
				return GroupingResult{}, err
			}
			for j := i + 1; j < len(groups); j++ {
				// Intersect upstream answers without reconstructing an ancestry graph.
				// Materialize combined proof paths only for the selected merge below.
				for ref, first := range scopes[i] {
					second, ok := scopes[j][ref]
					if !ok {
						continue
					}
					combined := append(append(Group(nil), groups[i]...), groups[j]...)
					if !fitsGroup(combined, in.DiffTokens) {
						blocked++
						break
					}
					size := 0
					for _, f := range combined {
						size += len(f.Diff)
					}
					depth := max(first.Depth, second.Depth)
					if left < 0 || depth < bestDepth || depth == bestDepth && (size < bestSize || size == bestSize && (i < left || i == left && (j < right || j == right && ref.NodeID < namespace.NodeID))) {
						left, right, bestDepth, bestSize, namespace = i, j, depth, size, ref
					}
				}
			}
		}
		result.BudgetBlocked = blocked
		if left < 0 {
			break
		}
		groups[left] = append(append(Group(nil), groups[left]...), groups[right]...)
		scopes[left] = commonNamespaces(scopes[left], scopes[right])
		var targets []string
		for _, f := range groups[left] {
			targets = append(targets, unit.FragmentID(f))
		}
		sort.Strings(targets)
		result.Merges = append(result.Merges, GroupingMerge{Namespace: namespace, Targets: targets, Paths: scopes[left][namespace].Paths})
		groups = append(groups[:right], groups[right+1:]...)
		scopes = append(scopes[:right], scopes[right+1:]...)
	}
	result.Groups = groups
	return result, nil
}

func commonNamespaces(a, b map[language.NamespaceRef]cg.NamespaceMatch) map[language.NamespaceRef]cg.NamespaceMatch {
	out := map[language.NamespaceRef]cg.NamespaceMatch{}
	for key, first := range a {
		if second, ok := b[key]; ok {
			match := first
			match.Depth = max(first.Depth, second.Depth)
			match.Confidence = first.Confidence.Weaker(second.Confidence)
			// A declaration or Document can anchor several fragments. Retain one proof
			// per input identity instead of duplicating its source context in every merge.
			proofs := map[string]cg.Path{}
			for _, path := range append(append([]cg.Path(nil), first.Paths...), second.Paths...) {
				proofs[path.Nodes[0].ID] = path
			}
			ids := make([]string, 0, len(proofs))
			for id := range proofs {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			match.Paths = make([]cg.Path, 0, len(ids))
			for _, id := range ids {
				match.Paths = append(match.Paths, proofs[id])
			}
			out[key] = match
		}
	}
	return out
}
