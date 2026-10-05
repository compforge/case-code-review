package language

import (
	"sort"

	cg "github.com/compforge/codegraph"
)

// AnchorNode keeps a captured declaration's publication identity through review.
func (r *RepositoryIndex) AnchorNode(a Anchor) (cg.Node, bool) {
	if r == nil || r.Graph == nil || a.Snapshot != r.Graph.Snapshot() {
		return cg.Node{}, false
	}
	return r.Graph.Node(a.NodeID)
}

func (r *RepositoryIndex) NodeLabel(id string) string {
	if r == nil || r.Graph == nil {
		return ""
	}
	n, ok := r.Graph.Node(id)
	if !ok {
		return ""
	}
	return ReviewSymbolID(n)
}

// ContractKey crosses into the authored catalog's name-based address space.
// Ambiguous names cannot attach a catalog entry to one of several declarations.
func (r *RepositoryIndex) ContractKey(id string) string {
	label := r.NodeLabel(id)
	if n, ok := r.Declaration(label); ok && n.ID == id {
		return label
	}
	return ""
}

func (r *RepositoryIndex) OwnerNodeIDs(id string) []string {
	if r == nil || r.Graph == nil {
		return nil
	}
	var out []string
	for _, kind := range []cg.RelationKind{cg.Contains, cg.Encloses} {
		for _, e := range r.Graph.RelationsTo(id, kind) {
			n, ok := r.Graph.Node(e.Source)
			if ok && e.Confidence.AtLeast(cg.Scoped) && ReviewSymbolID(n) != "" {
				out = append(out, n.ID)
			}
		}
		if len(out) > 0 {
			break
		}
	}
	sort.Strings(out)
	return out
}

func (r *RepositoryIndex) CallNodeNeighbors(id string, incoming bool) []string {
	if r == nil || r.Graph == nil {
		return nil
	}
	edges := r.Graph.RelationsFrom(id, cg.Calls)
	if incoming {
		edges = r.Graph.RelationsTo(id, cg.Calls)
	}
	seen := map[string]bool{}
	for _, edge := range edges {
		if !edge.Confidence.AtLeast(cg.Scoped) {
			continue
		}
		target := edge.Target
		if incoming {
			target = edge.Source
		}
		n, ok := r.Graph.Node(target)
		if !ok || target == id {
			continue
		}
		kind, ok := reviewKind(n.Kind)
		if ok && (kind == KindFunction || kind == KindMethod) {
			seen[target] = true
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (r *RepositoryIndex) NodeDoc(id string) string {
	if r == nil || r.Graph == nil {
		return ""
	}
	n, ok := r.Graph.Node(id)
	if !ok {
		return ""
	}
	return declarationDoc(n)
}
