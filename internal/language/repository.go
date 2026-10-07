package language

import (
	"context"
	"errors"
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
	Sources     map[string]string
}

func ScanRepository(repoDir string) *RepositoryIndex { return NewAnalyzer(repoDir).Repository() }

func (a *Analyzer) Repository() *RepositoryIndex {
	a.repositoryOnce.Do(func() {
		var observe func(*RepositoryIndex)
		if a.ObserveRepositoryBuild != nil {
			observe = a.ObserveRepositoryBuild()
		}
		start := time.Now()
		index := a.scanRepository()
		index.Duration = time.Since(start)
		a.repository.Store(index)
		if observe != nil {
			observe(index)
		}
	})
	return a.repository.Load()
}

func (a *Analyzer) scanRepository() *RepositoryIndex {
	out := &RepositoryIndex{Definitions: map[string][]IndexedDefinition{}, Sources: map[string]string{}}
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
	// Changed materials are admitted first, even if a workspace file was moved
	// after GetDiff. Remaining repository material is deterministic and bounded.
	present := map[string]bool{}
	for i := range entries {
		if content, ok := a.documents[entries[i].path]; ok {
			entries[i].size = int64(len(content))
		}
		present[entries[i].path] = true
	}
	for path, content := range a.documents {
		if !present[path] {
			entries = append(entries, repositoryEntry{path: path, size: int64(len(content))})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		// Module identity is needed even when source admission reaches its budget.
		aMod, bMod := filepath.Base(entries[i].path) == "go.mod", filepath.Base(entries[j].path) == "go.mod"
		if aMod != bMod {
			return aMod
		}
		_, aChanged := a.documents[entries[i].path]
		_, bChanged := a.documents[entries[j].path]
		if aChanged != bChanged {
			return aChanged
		}
		return entries[i].path < entries[j].path
	})
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
	}
	opts := cg.Options{MaxDocuments: maxScanFiles, MaxDocumentBytes: maxFileBytes, ResolutionContext: cg.ResolutionContext{GoModules: modules}}
	var admitted int
	out.Graph, out.Report, admitted, err = buildBoundedGraph(ctx, snapshot, facts, opts)
	if admitted < len(facts) {
		out.Gaps = append(out.Gaps, fmt.Sprintf("graph budget: omitted %d of %d parsed documents; changed documents were prioritized", len(facts)-admitted, len(facts)))
	}
	if err != nil {
		out.Gaps = append(out.Gaps, fmt.Sprintf("build graph: %v", err))
	}
	if out.Graph != nil {
		// Project the publication once. Repository ranking does not need to construct
		// a per-file outline or repeatedly scan the full graph's adjacency.
		for _, node := range out.Graph.Nodes() {
			if node.Location == nil || isRepositoryTestFile(filepath.Base(node.Location.Path)) {
				continue
			}
			path := node.Location.Path
			if d, ok := reviewDefinition(path, node); ok {
				out.Definitions[path] = append(out.Definitions[path], IndexedDefinition{Name: d.Name, SymbolID: d.SymbolID, Path: path, Line: d.Span.Start, Signature: d.Signature})
			}
		}
		for path := range out.Definitions {
			sort.SliceStable(out.Definitions[path], func(i, j int) bool { return out.Definitions[path][i].Line < out.Definitions[path][j].Line })
		}
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

	node, ok := r.Declaration(symbol)
	if !ok {
		return nil
	}
	var result []string
	for _, id := range r.CallNodeNeighbors(node.ID, incoming) {
		if key := r.ContractKey(id); key != "" {
			result = append(result, key)
		}
	}
	sort.Strings(result)
	return result
}

// Source byte limits alone cannot bound graph density. A failed publication is
// retried on a deterministic prefix, keeping the changed-document priority and
// the original deadline. Only a budget failure authorizes reducing coverage.
func buildBoundedGraph(ctx context.Context, snapshot string, facts []cg.Facts, opts cg.Options) (*cg.Graph, cg.BuildReport, int, error) {
	for n := len(facts); ; n /= 2 {
		builder, err := cg.NewBuilder(snapshot, opts)
		if err == nil {
			err = builder.Add(facts[:n]...)
		}
		var graph *cg.Graph
		var report cg.BuildReport
		if err == nil {
			graph, report, err = builder.Build(ctx)
		}
		if err == nil || !errors.Is(err, cg.ErrBuildBudget) || n <= 1 || ctx.Err() != nil {
			return graph, report, n, err
		}
	}
}
