package language

import (
	"strings"

	cg "github.com/compforge/codegraph"
	gotreesitter "github.com/odvcencio/gotreesitter"
)

// outlineEntries adds only CCR presentation to CodeGraph's upstream outline.
func outlineEntries(source Source, symbols []gotreesitter.OutlineSymbol) []outlineEntry {
	entries := make([]outlineEntry, 0, len(symbols))
	var appendSymbols func([]gotreesitter.OutlineSymbol, string)
	appendSymbols = func(symbols []gotreesitter.OutlineSymbol, parent string) {
		for _, symbol := range symbols {
			owner := parent
			if owner == "" {
				owner = symbol.Owner
			}
			name := symbol.Name
			if owner != "" {
				name = owner + "." + name
			}
			entries = append(entries, outlineEntry{
				Name:      name,
				Owner:     owner,
				Label:     symbol.Kind,
				Signature: sourceSignature(source.Content, symbol.Range.StartByte, symbol.Range.EndByte),
				Span:      byteSpan(source.Content, symbol.Range.StartByte, symbol.Range.EndByte),
				CanOwn:    len(symbol.Children) > 0 || canOwnOutlineChildren(symbol.Kind),
			})
			appendSymbols(symbol.Children, name)
		}
	}
	appendSymbols(symbols, "")
	return entries
}

// CodeGraph's Go declarations include type/field facts absent from the generic
// outline query. The review view can display them without a second parser.
func reviewOutlineEntries(source Source, facts cg.Facts, symbols []gotreesitter.OutlineSymbol) []outlineEntry {
	entries := outlineEntries(source, symbols)
	if facts.Language != "go" {
		return entries
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.Name] = true
	}
	for _, decl := range facts.Declarations {
		name := decl.QualifiedName
		if seen[name] {
			continue
		}
		label := ""
		switch decl.Kind {
		case cg.Struct, cg.Type, cg.TypeAlias, cg.Interface:
			label = "type"
		case cg.Field:
			label = "field"
		default:
			continue
		}
		owner := ""
		if i := strings.LastIndex(name, "."); i >= 0 {
			owner = name[:i]
		}
		signature := sourceSignature(source.Content, uint32(decl.Location.StartByte), uint32(decl.Location.EndByte))
		if label == "type" {
			signature = "type " + signature
		}
		entries = append(entries, outlineEntry{Name: name, Owner: owner, Label: label, Signature: signature, Span: locationSpan(decl.Location), CanOwn: label == "type"})
	}
	return entries
}
