package language

import (
	gotreesitter "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

// treeSitterOutlineEntries maps gotreesitter's language-level outline into
// CCR's file-level navigation model. CCR owns presentation and consumption;
// the grammar library owns non-Go source-language coverage.
func treeSitterOutlineEntries(
	source Source,
	tree *gotreesitter.Tree,
	entry grammars.LangEntry,
) ([]outlineEntry, bool) {
	outliner, err := gotreesitter.NewOutliner(
		tree.Language(),
		grammars.ResolveTagsQuery(entry),
		gotreesitter.WithOutlineOwnerRules(grammars.OutlineOwnerRules(entry)),
	)
	if err != nil {
		return nil, false
	}
	symbols, report := outliner.OutlineTree(tree)
	if report.Declined() {
		return nil, false
	}

	entries := make([]outlineEntry, 0, report.Symbols)
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
				Signature: treeSitterSignature(source.Content, symbol.Range.StartByte, symbol.Range.EndByte),
				Span:      byteSpan(source.Content, symbol.Range.StartByte, symbol.Range.EndByte),
				CanOwn:    len(symbol.Children) > 0 || canOwnOutlineChildren(symbol.Kind),
			})
			appendSymbols(symbol.Children, name)
		}
	}
	appendSymbols(symbols, "")
	return entries, true
}
