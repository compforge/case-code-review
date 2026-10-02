package sourcecontext

import (
	"github.com/qiankunli/case-code-review/internal/language"
)

// Scan projects the shared repository snapshot into CCR's relevance-ranking view.
func Scan(analyzer *language.Analyzer) *Extraction {
	index := analyzer.Repository()
	extraction := &Extraction{Defs: map[string][]Def{}, Refs: index.References, Graph: index.Graph}
	for path, definitions := range index.Definitions {
		for _, definition := range definitions {
			extraction.Defs[path] = append(extraction.Defs[path], Def{
				Ident: definition.Name, SymbolID: definition.SymbolID,
				File: definition.Path, Line: definition.Line, Signature: definition.Signature,
			})
		}
	}
	return extraction
}
