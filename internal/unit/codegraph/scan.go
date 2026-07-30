package codegraph

import (
	"strings"

	"github.com/qiankunli/case-code-review/internal/language"
)

// Scan converts the language-owned repository index into codegraph's ranking
// model. Parser details stay behind internal/language; only this adapter knows
// both fact shapes.
func Scan(repoDir string) *Extraction {
	index := language.ScanRepository(repoDir)
	extraction := &Extraction{Defs: map[string][]Def{}, Refs: index.References}
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

// PairMethodIdents is a ranking policy, not a parser fact: call sites mention a
// bare method name, so method definitions receive a second pairing alias.
func PairMethodIdents(ex *Extraction) {
	for path, definitions := range ex.Defs {
		for _, definition := range definitions {
			if i := strings.LastIndex(definition.Ident, "."); i > 0 {
				alias := definition
				alias.Ident = definition.Ident[i+1:]
				ex.Defs[path] = append(ex.Defs[path], alias)
			}
		}
	}
}
