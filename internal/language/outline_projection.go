package language

import (
	"strings"

	cg "github.com/compforge/codegraph"
)

// CCR presents lexical structure from encloses. A Go receiver method may also
// be grouped under a same-file type using the independently proven contains edge.
func graphOutlineEntries(graph *cg.Graph, path string) []outlineEntry {
	nodes := graph.Find(path, "", "")
	byID := make(map[string]cg.Node, len(nodes))
	for _, node := range nodes {
		byID[node.ID] = node
	}
	lexical, semantic := map[string]string{}, map[string]string{}
	owns := map[string]bool{}
	for _, edge := range graph.Relations() {
		parent, ok := byID[edge.Source]
		if !ok {
			continue
		}
		if _, ok := byID[edge.Target]; !ok {
			continue
		}
		switch edge.Kind {
		case cg.Encloses:
			owns[edge.Source] = true
			lexical[edge.Target] = parent.QualifiedName
		case cg.Contains:
			owns[edge.Source] = true
			semantic[edge.Target] = parent.QualifiedName
		}
	}
	var entries []outlineEntry
	for _, node := range nodes {
		owner := lexical[node.ID]
		if owner == "" {
			owner = semantic[node.ID]
		}

		label := strings.ToLower(string(node.Kind))
		signature := displaySignature(node.Signature)
		if node.Language == "go" {
			switch node.Kind {
			case cg.Struct, cg.Interface, cg.Type, cg.TypeAlias:
				label = "type"
				if signature != "" {
					signature = "type " + signature
				}
			}
		}
		entries = append(entries, outlineEntry{Name: node.QualifiedName, Owner: owner, Label: label, Signature: signature, Span: locationSpan(*node.Location), CanOwn: owns[node.ID] || canOwnOutlineChildren(label)})
	}
	return entries
}

// Header parsing belongs to CodeGraph; whitespace and display budgets belong to CCR.
func displaySignature(signature string) string {
	value := strings.Join(strings.Fields(signature), " ")
	runes := []rune(value)
	if len(runes) > 240 {
		return string(runes[:237]) + "..."
	}
	return value
}
