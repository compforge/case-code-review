package language

import cg "github.com/compforge/codegraph"

// Connection is a graph-backed grouping candidate. Touched distinguishes an
// occurrence on changed lines from a declaration's existing dependency.
type Connection struct {
	Snapshot   string          `json:"snapshot"`
	Source     string          `json:"source"`
	Target     string          `json:"target"`
	SourcePath string          `json:"source_path,omitempty"`
	TargetPath string          `json:"target_path,omitempty"`
	Kind       cg.RelationKind `json:"kind"`
	Confidence cg.Confidence   `json:"confidence"`
	Touched    bool            `json:"touched"`
}

func (r *RepositoryIndex) Connections(owners []Anchor, path string, spans []Span) []Connection {
	if r == nil || r.Graph == nil {
		return nil
	}
	g := r.Graph
	var out []Connection
	add := func(source, target string, kind cg.RelationKind, confidence cg.Confidence, touched bool) {
		s, _ := g.Node(source)
		t, _ := g.Node(target)
		sourcePath, targetPath := "", ""
		if s.Location != nil {
			sourcePath = s.Location.Path
		}
		if t.Location != nil {
			targetPath = t.Location.Path
		}
		out = append(out, Connection{Snapshot: g.Snapshot(), Source: source, Target: target, SourcePath: sourcePath, TargetPath: targetPath, Kind: kind, Confidence: confidence, Touched: touched})
	}
	// Keep binding nodes along the path: changes to a barrel/import can matter
	// even when the final declaration itself was not modified.
	follow := func(source, target string, kind cg.RelationKind, confidence cg.Confidence, touched bool) {
		type hop struct {
			id         string
			confidence cg.Confidence
		}
		queue := []hop{{target, confidence}}
		seen := map[string]bool{}
		for len(queue) > 0 {
			item := queue[0]
			id := item.id
			queue = queue[1:]
			if seen[id] {
				continue
			}
			seen[id] = true
			add(source, id, kind, item.confidence, touched)
			for _, e := range g.RelationsFrom(id, cg.Aliases) {
				if e.Confidence.AtLeast(cg.Scoped) {
					next := item.confidence
					if next.AtLeast(e.Confidence) {
						next = e.Confidence
					}
					queue = append(queue, hop{e.Target, next})
				}
			}
		}
	}
	for _, owner := range owners {
		for _, e := range g.RelationsFrom(owner.NodeID, cg.Calls, cg.References, cg.Extends, cg.Implements, cg.Aliases, cg.Exports) {
			if e.Confidence.AtLeast(cg.Scoped) {
				follow(e.Source, e.Target, e.Kind, e.Confidence, false)
			}
		}
	}
	for _, n := range g.Nodes() {
		if n.Kind != cg.Reference || n.Location == nil || n.Location.Path != path {
			continue
		}
		touched := false
		for _, span := range spans {
			if n.Location.Line <= span.End && locationSpan(*n.Location).End >= span.Start {
				touched = true
				break
			}
		}
		if !touched {
			continue
		}
		for _, e := range g.RelationsFrom(n.ID, cg.References) {
			if e.Confidence.AtLeast(cg.Scoped) {
				follow(n.ID, e.Target, e.Kind, e.Confidence, true)
			}
		}
	}
	return out
}
