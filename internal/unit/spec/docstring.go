package spec

import (
	"os"
	"path/filepath"

	"github.com/qiankunli/case-code-review/internal/language"
)

// SymbolDocstring reads a symbol's docstring from its source file in the repo
// (adoption-free — no marker needed), given its symbol-id `<relpath>::<name>`:
// a language-native source comment. "" when the language isn't supported, the
// file isn't readable, or the symbol has no docstring.
// Shared by the owner/used relations and the callgraph caller/callee walk.
func SymbolDocstring(repoDir, symbolID string) string {
	if repoDir == "" {
		return ""
	}
	rel, name, ok := language.SplitSymbolID(symbolID)
	if !ok {
		return ""
	}
	return extractDocFromFile(filepath.Join(repoDir, rel), name)
}

// extractDocFromFile extracts name's summary docstring from the source file at
// path, dispatching by extension. Best-effort: any failure yields "".
func extractDocFromFile(path, name string) string {
	src, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return language.NewAnalyzer("").Doc(language.Source{Path: path, Content: string(src)}, name)
}
