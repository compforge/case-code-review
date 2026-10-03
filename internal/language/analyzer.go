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
	documents         map[string]string // captured changed documents, configured before publication
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
	path := documentPath(source.Path)
	return a.extractor.Extract(ctx, cg.Document{Path: path, Content: []byte(source.Content)})
}

func (a *Analyzer) Analyze(ctx context.Context, source Source) (Analysis, error) {
	facts, err := a.extract(ctx, source)
	if err != nil {
		return Analysis{}, err
	}
	graph, err := documentGraph(ctx, facts)
	if err != nil {
		return Analysis{}, err
	}
	return projectAnalysis(source, graph), nil
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
	graph, err := documentGraph(ctx, facts)
	if err != nil {
		return FileOutline{}, err
	}
	doc, _ := graph.Node(cg.DocumentID(documentPath(source.Path)))
	return FileOutline{Path: source.Path, Language: Language(doc.Language), entries: graphOutlineEntries(graph, documentPath(source.Path))}, nil
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
	facts, err := a.extract(context.Background(), source)
	if err != nil {
		return ""
	}
	graph, err := documentGraph(context.Background(), facts)
	if err != nil {
		return ""
	}
	nodes := graph.Find(documentPath(source.Path), "", symbol)
	if len(nodes) != 1 {
		return ""
	}
	return declarationDoc(nodes[0])
}

// RepositoryDoc reads documentation from the publication that supplied relations.
func (a *Analyzer) RepositoryDoc(id string) string {
	if n, ok := a.Repository().Declaration(id); ok {
		return declarationDoc(n)
	}
	return ""
}

func documentPath(path string) string { return strings.TrimPrefix(filepath.ToSlash(path), "/") }

// Document navigation consumes a publication built from the shared extraction
// cache; it never depends on the parser's intermediate outline representation.
func documentGraph(ctx context.Context, facts cg.Facts) (*cg.Graph, error) {
	builder, err := cg.NewBuilder("document", cg.Options{})
	if err != nil {
		return nil, err
	}
	if err := builder.Add(facts); err != nil {
		return nil, err
	}
	graph, _, err := builder.Build(ctx)
	return graph, err
}

// SetDocuments prioritizes captured diff inputs over subsequent workspace reads.
// Configure once, before the analyzer is shared with review consumers.
func (a *Analyzer) SetDocuments(documents map[string]string) { a.documents = documents }

func (a *Analyzer) Anchors(ctx context.Context, source Source) ([]Anchor, error) {
	facts, err := a.extract(ctx, source)
	if err != nil {
		return nil, err
	}
	graph, err := documentGraph(ctx, facts)
	if err != nil {
		return nil, err
	}
	anchors := graphAnchors(graph, documentPath(source.Path))
	snapshot := a.ref
	if snapshot == "" {
		snapshot = "review-worktree"
	}
	for i := range anchors {
		anchors[i].Snapshot = snapshot
	}
	return anchors, nil
}

// Capture pins the provider's resolved revision and changed bytes before use.
func (a *Analyzer) Capture(ref string, documents map[string]string) {
	a.ref = ref
	a.documents = documents
}
