package language

import (
	"sort"

	cg "github.com/compforge/codegraph"
)

// Anchor identifies a source owner inside one publication. Before and after
// anchors are kept separately by Fragment; a matching name is not a version join.
type Anchor struct {
	Snapshot  string      `json:"snapshot"`
	NodeID    string      `json:"node_id"`
	SymbolID  string      `json:"symbol_id,omitempty"`
	Path      string      `json:"path"`
	Kind      cg.NodeKind `json:"kind"`
	Span      Span        `json:"span"`
	StartByte int         `json:"start_byte"`
	EndByte   int         `json:"end_byte"`
}

func graphAnchors(g *cg.Graph, path string) []Anchor {
	var out []Anchor
	for _, n := range g.Nodes() {
		if n.Location == nil || n.Location.Path != path || n.Kind == cg.Reference || n.Kind == cg.DocumentKind {
			continue
		}
		anchor := Anchor{Snapshot: g.Snapshot(), NodeID: n.ID, SymbolID: ReviewSymbolID(n), Path: path, Kind: n.Kind, Span: locationSpan(*n.Location), StartByte: n.Location.StartByte, EndByte: n.Location.EndByte}
		out = append(out, anchor)
		// Authored documentation belongs to its declaration even when it precedes
		// the declaration span. Ownership comes from graph properties, not text scans.
		addLocation := func(loc cg.Location) {
			if loc.Path != path {
				return
			}
			a := anchor
			a.Span, a.StartByte, a.EndByte = locationSpan(loc), loc.StartByte, loc.EndByte
			out = append(out, a)
		}
		for _, doc := range n.Documentation {
			addLocation(doc.Location)
		}
		for _, marker := range n.Markers {
			addLocation(marker.Location)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.EndByte-a.StartByte != b.EndByte-b.StartByte {
			return a.EndByte-a.StartByte < b.EndByte-b.StartByte
		}
		return a.NodeID < b.NodeID
	})
	return out
}

// AnchorAt chooses the innermost retained owner. Occurrences supply relationship
// evidence, rather than becoming a separate review target for every identifier.
func AnchorAt(anchors []Anchor, line int) (Anchor, bool) {
	for _, a := range anchors {
		if a.Span.Start <= line && line <= a.Span.End {
			return a, true
		}
	}
	return Anchor{}, false
}
