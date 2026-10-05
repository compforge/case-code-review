package language

import (
	"context"
	"fmt"

	cg "github.com/compforge/codegraph"
)

// NamespaceRef identifies a semantic container in one graph snapshot. Names and
// directories alone cannot identify packages or bridge before/after graphs.
type NamespaceRef struct {
	Snapshot string `json:"snapshot"`
	NodeID   string `json:"node_id"`
	Name     string `json:"name"`
}

// CommonNamespaces adapts CCR anchors to CodeGraph's ownership query. With no
// anchors, the Document is the input. Hierarchy, distances and proof paths are
// upstream facts; CCR selects the organizational kinds allowed for unit grouping.
func (r *RepositoryIndex) CommonNamespaces(ctx context.Context, anchors []Anchor, path string) (map[NamespaceRef]cg.NamespaceMatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.Graph == nil {
		return nil, nil
	}
	g := r.Graph
	ids := make([]string, 0, len(anchors))
	for _, a := range anchors {
		if a.Snapshot != g.Snapshot() {
			return nil, fmt.Errorf("namespace anchor snapshot %q does not match graph %q", a.Snapshot, g.Snapshot())
		}
		ids = append(ids, a.NodeID)
	}
	if len(ids) == 0 {
		ids = append(ids, cg.DocumentID(path))
	}
	matches, err := g.CommonNamespaces(ctx, ids, cg.NamespaceOptions{Kinds: []cg.NodeKind{cg.Package, cg.Module, cg.Namespace}, MinConfidence: cg.Exact})
	if err != nil {
		return nil, fmt.Errorf("namespace ownership for %s: %w", path, err)
	}
	out := make(map[NamespaceRef]cg.NamespaceMatch, len(matches))
	for _, match := range matches {
		out[NamespaceRef{g.Snapshot(), match.Node.ID, match.Node.Name}] = match
	}
	return out, nil
}
