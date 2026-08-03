package language

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// ErrUnsupported is returned for paths without a registered source language.
var ErrUnsupported = errors.New("unsupported source language")

// Analyzer is the stable entry point for source-language analysis. Backend
// selection and project-local tooling stay private so consumers are unaffected
// when legacy parsers are replaced by gotreesitter.
type Analyzer struct {
	repoDir string
	mu      sync.Mutex
	cache   map[analysisKey]Analysis
}

// FileOutline returns a language/file-format-aware structural projection.
// Source analyzers reuse their cached facts; data and document formats keep
// their own lightweight shape without pretending to be code definitions.
func (a *Analyzer) FileOutline(ctx context.Context, source Source) (FileOutline, error) {
	switch strings.ToLower(filepath.Ext(source.Path)) {
	case ".json":
		return jsonFileOutline(source)
	case ".md", ".markdown":
		return markdownFileOutline(source), nil
	}
	analysis, err := a.Analyze(ctx, source)
	if err != nil {
		return FileOutline{}, err
	}
	return analysis.Outline(source.Path), nil
}

type analysisKey struct {
	path   string
	digest [32]byte
}

func NewAnalyzer(repoDir string) *Analyzer {
	return &Analyzer{repoDir: repoDir, cache: map[analysisKey]Analysis{}}
}

// Analyze extracts parser-independent facts from one source file.
func (a *Analyzer) Analyze(ctx context.Context, source Source) (Analysis, error) {
	lang, ok := Detect(source.Path)
	if !ok {
		return Analysis{}, fmt.Errorf("%w: %s", ErrUnsupported, source.Path)
	}
	key := analysisKey{path: source.Path, digest: sha256.Sum256([]byte(source.Content))}
	a.mu.Lock()
	if a.cache == nil {
		a.cache = map[analysisKey]Analysis{}
	}
	analysis, cached := a.cache[key]
	a.mu.Unlock()
	if cached {
		return analysis, nil
	}
	var err error
	switch lang {
	case Go:
		analysis, err = analyzeGo(source)
	case Python:
		analysis, err = analyzePython(ctx, source)
	default:
		analysis, err = analyzeTreeSitter(ctx, lang, source)
	}
	if err != nil {
		return Analysis{}, err
	}
	a.mu.Lock()
	a.cache[key] = analysis
	a.mu.Unlock()
	return analysis, nil
}

// DefinitionAt resolves a source line to its enclosing callable definition.
func (a *Analyzer) DefinitionAt(ctx context.Context, source Source, line int) (Definition, bool) {
	analysis, err := a.Analyze(ctx, source)
	if err != nil {
		return Definition{}, false
	}
	return analysis.DefinitionAt(line)
}

// DefinitionByID resolves a canonical symbol id in a source file.
func (a *Analyzer) DefinitionByID(ctx context.Context, source Source, id string) (Definition, bool) {
	analysis, err := a.Analyze(ctx, source)
	if err != nil {
		return Definition{}, false
	}
	return analysis.DefinitionByID(id)
}

// CalleesOf returns unresolved call names made by the requested definition.
func (a *Analyzer) CalleesOf(ctx context.Context, source Source, symbol string) []string {
	analysis, err := a.Analyze(ctx, source)
	if err != nil {
		return nil
	}
	return analysis.CalleesOf(symbol)
}

// Doc returns the first-paragraph documentation attached to a symbol. It is a
// language fact, so callers need not know whether comments or string literals
// carry documentation in the underlying grammar.
func (a *Analyzer) Doc(source Source, symbol string) string {
	lang, ok := Detect(source.Path)
	if !ok {
		return ""
	}
	switch lang {
	case Go:
		return extractGoDoc(source.Content, symbol)
	case Python:
		return extractPyDocstring(source.Content, symbol)
	default:
		return ""
	}
}
