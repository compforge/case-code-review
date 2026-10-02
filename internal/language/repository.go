package language

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cg "github.com/compforge/codegraph"
	"golang.org/x/mod/modfile"
)

const (
	maxScanFiles = 2000
	maxFileBytes = 512 * 1024
)

type IndexedDefinition struct {
	Name, SymbolID, Path string
	Line                 int
	Signature            string
}

// RepositoryIndex is one review's bounded source snapshot and its CodeGraph.
// Selection and loading belong to CCR; extraction and binding belong upstream.
type RepositoryIndex struct {
	Duration    time.Duration
	Graph       *cg.Graph
	Report      cg.BuildReport
	Gaps        []string
	Definitions map[string][]IndexedDefinition
	References  map[string]map[string]int
	Sources     map[string]string
}

func ScanRepository(repoDir string) *RepositoryIndex { return NewAnalyzer(repoDir).Repository() }

func (a *Analyzer) Repository() *RepositoryIndex {
	a.repositoryOnce.Do(func() {
		start := time.Now()
		a.repository = a.scanRepository()
		a.repository.Duration = time.Since(start)
		if a.OnRepositoryBuilt != nil {
			a.OnRepositoryBuilt(a.repository)
		}
	})
	return a.repository
}

func (a *Analyzer) scanRepository() *RepositoryIndex {
	out := &RepositoryIndex{Definitions: map[string][]IndexedDefinition{}, References: map[string]map[string]int{}, Sources: map[string]string{}}
	if a.repoDir == "" {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var facts []cg.Facts
	modules := map[string]string{}
	count := 0
	var bytes int64
	entries, snapshot, err := a.repositoryEntries(ctx)
	if err != nil {
		out.Gaps = append(out.Gaps, err.Error())
		return out
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			out.Gaps = append(out.Gaps, ctx.Err().Error())
			break
		}
		rel := entry.path
		name := filepath.Base(rel)

		if name == "go.mod" {
			data, err := a.readRepositoryFile(ctx, snapshot, rel)
			if err != nil {
				out.Gaps = append(out.Gaps, rel+": "+err.Error())
				continue
			}
			if module := modfile.ModulePath(data); module != "" {
				modules[filepath.ToSlash(filepath.Dir(rel))] = module
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(rel))
		if !IsReviewableExtension(ext) || fileScopeExtensions[ext] {
			continue
		}
		if _, ok := Detect(rel); !ok {
			continue
		}
		if entry.size > maxFileBytes {
			out.Gaps = append(out.Gaps, rel+": document byte limit")
			continue
		}
		if count >= maxScanFiles || bytes+entry.size > 32<<20 {
			out.Gaps = append(out.Gaps, "repository source budget reached")
			break
		}
		count++
		content, err := a.readRepositoryFile(ctx, snapshot, rel)
		if err != nil {
			out.Gaps = append(out.Gaps, rel+": "+err.Error())
			continue
		}
		bytes += int64(len(content))
		source := Source{Path: rel, Content: string(content)}
		f, err := a.extract(ctx, source)
		if err != nil {
			out.Gaps = append(out.Gaps, rel+": "+err.Error())
			continue
		}
		facts = append(facts, f)
		out.Sources[rel] = source.Content
		analysis := projectAnalysis(source, f)
		// Tests remain graph evidence (callers/usages), but do not dominate repo-map ranking.
		if !isRepositoryTestFile(name) {
			out.References[rel] = analysis.References
			for _, d := range analysis.Definitions {
				out.Definitions[rel] = append(out.Definitions[rel], IndexedDefinition{Name: d.Name, SymbolID: d.SymbolID, Path: rel, Line: d.Span.Start, Signature: d.Signature})
			}
		}
	}
	builder, err := cg.NewBuilder(snapshot, cg.Options{MaxDocuments: maxScanFiles, MaxDocumentBytes: maxFileBytes, ResolutionContext: cg.ResolutionContext{GoModules: modules}})
	if err == nil {
		err = builder.Add(facts...)
	}
	if err == nil {
		out.Graph, out.Report, err = builder.Build(ctx)
	}
	if err != nil {
		out.Gaps = append(out.Gaps, fmt.Sprintf("build graph: %v", err))
	}
	return out
}

var repositorySkipDirs = map[string]bool{"vendor": true, "node_modules": true, "testdata": true, "__pycache__": true, "venv": true, "site-packages": true}

func isRepositoryTestFile(name string) bool {
	lower := strings.ToLower(name)
	base := strings.TrimSuffix(lower, filepath.Ext(lower))
	return strings.Contains(lower, ".test.") || strings.Contains(lower, ".spec.") || strings.HasPrefix(base, "test_") || strings.HasPrefix(base, "test-") || strings.HasSuffix(base, "_test") || strings.HasSuffix(base, "_spec")
}

// CallNeighbors admits only scoped or exact CodeGraph call evidence.
// Missing/partial graph evidence leaves Units separate; it never triggers name guessing.
func (r *RepositoryIndex) CallNeighbors(symbol string, incoming bool) []string {
	if r.Graph == nil {
		return nil
	}
	path, name, ok := SplitSymbolID(symbol)
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	nodes := r.Graph.Find(path, "", name)
	// CCR join keys cannot distinguish overloaded/redefined declarations.
	if len(nodes) != 1 {
		return nil
	}
	for _, node := range nodes {
		edges := r.Graph.RelationsFrom(node.ID, cg.Calls)
		if incoming {
			edges = r.Graph.RelationsTo(node.ID, cg.Calls)
		}
		for _, edge := range edges {
			if !edge.Confidence.AtLeast(cg.Scoped) {
				continue
			}
			id := edge.Target
			if incoming {
				id = edge.Source
			}
			neighbor, ok := r.Graph.Node(id)
			if !ok {
				continue
			}
			if kind, ok := reviewKind(neighbor.Kind); !ok || (kind != KindFunction && kind != KindMethod) {
				continue
			}
			if neighbor.Location == nil || len(r.Graph.Find(neighbor.Location.Path, "", neighbor.QualifiedName)) != 1 {
				continue
			}
			if id := ReviewSymbolID(neighbor); id != "" && id != symbol {
				seen[id] = true
			}
		}
	}
	result := make([]string, 0, len(seen))
	for id := range seen {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}
