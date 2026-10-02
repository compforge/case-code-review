package language

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/gitcmd"
)

var ErrUnsupported = errors.New("unsupported source language")

// Analyzer adapts CodeGraph's document analysis to CCR's review views.
// One bounded extraction cache is shared by navigation and the repository graph.
type Analyzer struct {
	// OnRepositoryBuilt observes the one published analysis result. Set before use.
	OnRepositoryBuilt func(*RepositoryIndex)
	ref               string
	git               *gitcmd.Runner
	repoDir           string
	extractor         *cg.Extractor
	repositoryOnce    sync.Once
	repository        *RepositoryIndex
}

func NewAnalyzer(repoDir string) *Analyzer {
	cache, _ := cg.NewExtractionCache(2000, 32<<20)
	extractor, _ := cg.NewExtractor(cg.ExtractionOptions{Cache: cache})
	return &Analyzer{repoDir: repoDir, extractor: extractor}
}

// NewSnapshotAnalyzer reads graph inputs from the reviewed Git ref. An empty
// ref selects working files. Configure this before the first Repository call.
func NewSnapshotAnalyzer(repoDir, ref string, runner *gitcmd.Runner) *Analyzer {
	a := NewAnalyzer(repoDir)
	a.ref, a.git = ref, runner
	if a.git == nil {
		a.git = gitcmd.New(0)
	}
	return a
}

func (a *Analyzer) extract(ctx context.Context, source Source) (cg.Facts, error) {
	if _, ok := Detect(source.Path); !ok {
		return cg.Facts{}, fmt.Errorf("%w: %s", ErrUnsupported, source.Path)
	}
	// External dependency navigation still needs a valid logical document path.
	path := filepath.ToSlash(source.Path)
	if filepath.IsAbs(path) {
		path = strings.TrimPrefix(path, "/")
	}
	return a.extractor.Extract(ctx, cg.Document{Path: path, Content: []byte(source.Content)})
}

func (a *Analyzer) Analyze(ctx context.Context, source Source) (Analysis, error) {
	facts, err := a.extract(ctx, source)
	if err != nil {
		return Analysis{}, err
	}
	return projectAnalysis(source, facts), nil
}

func (a *Analyzer) FileOutline(ctx context.Context, source Source) (FileOutline, error) {
	switch strings.ToLower(filepath.Ext(source.Path)) {
	case ".json":
		return jsonFileOutline(source)
	case ".md", ".markdown":
		return markdownFileOutline(source), nil
	}
	facts, err := a.extract(ctx, source)
	if err != nil {
		return FileOutline{}, err
	}
	symbols, report, err := facts.Outline()
	if err != nil {
		return FileOutline{}, err
	}
	if report.Declined() {
		return FileOutline{}, fmt.Errorf("outline unavailable for %s: %s", source.Path, report.DeclineReason)
	}
	return FileOutline{Path: source.Path, Language: Language(facts.Language), entries: reviewOutlineEntries(source, facts, symbols)}, nil
}

// DefinitionAt resolves a source line to its enclosing callable definition.
func (a *Analyzer) DefinitionAt(ctx context.Context, source Source, line int) (Definition, bool) {
	analysis, err := a.Analyze(ctx, source)
	if err != nil {
		return Definition{}, false
	}
	return analysis.DefinitionAt(line)
}

// SymbolAt resolves a source line to its innermost named definition, including
// non-callable types and classes.
func (a *Analyzer) SymbolAt(ctx context.Context, source Source, line int) (Definition, bool) {
	analysis, err := a.Analyze(ctx, source)
	if err != nil {
		return Definition{}, false
	}
	return analysis.SymbolAt(line)
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
// review documentation, so callers need not know whether comments or string literals
// carry documentation in the underlying grammar.
func (a *Analyzer) Doc(source Source, symbol string) string {
	lang, ok := Detect(source.Path)
	if !ok {
		return ""
	}
	switch lang {
	case Go:
		return a.goDoc(source, symbol)
	case Python:
		return extractPyDocstring(source.Content, symbol)
	default:
		return ""
	}
}

// RepositoryDoc renders documentation from the same captured sources as call edges.
func (a *Analyzer) RepositoryDoc(id string) string {
	path, name, ok := SplitSymbolID(id)
	if !ok {
		return ""
	}
	source, ok := a.Repository().Sources[path]
	if !ok {
		return ""
	}
	return a.Doc(Source{Path: path, Content: source}, name)
}
