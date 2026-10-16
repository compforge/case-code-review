package language

import (
	"context"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repocli/toolkit/go"
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
// CCR owns review admission and projection; material loading and code facts belong upstream.
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
		a.repository = a.scanRepository()
		a.repository.Duration = time.Since(start)
		if observe != nil {
			observe(a.repository)
		}
	})
	return a.repository
}

func (a *Analyzer) scanRepository() *RepositoryIndex {
	out := &RepositoryIndex{Definitions: map[string][]IndexedDefinition{}, Sources: map[string]string{}}
	if a.repoDir == "" {
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var source repocli.GraphSource
	var err error
	if a.snapshot != nil {
		source = a.snapshot
	} else {
		source, err = repocli.OpenGraphSource(ctx, repocli.InputRequest{Repository: a.repoDir, Head: a.ref})
		if err == nil {
			var view reviewGraphSource
			view, err = newReviewGraphSource(source, a.documents)
			if a.ref == "" {
				view.id = "review-worktree"
			}
			source = view
		}
	}
	if err != nil {
		out.Gaps = append(out.Gaps, err.Error())
		return out
	}
	priority := make([]string, 0, len(a.documents))
	for name := range a.documents {
		priority = append(priority, name)
	}
	result, err := repocli.GraphFromSource(ctx, source, repocli.GraphOptions{
		RetryOnBudget: true,
		CodeGraph: cg.Options{
			MaxDocuments:     maxScanFiles,
			MaxDocumentBytes: maxFileBytes,
			MaxSourceBytes:   32 << 20,
			ExtractionCache:  a.extractionCache,
			Admission: &cg.AdmissionPolicy{ExcludedTags: []cg.Tag{
				cg.DependencyTag, cg.TestFixtureTag, cg.GeneratedTag,
				cg.ToolingTag, cg.CacheTag, cg.BuildOutputTag,
			}},
		},
		Prioritize: priority,
		Select: func(name string, tags []cg.Tag) bool {
			if !repocli.IsSourceMaterial(name) {
				return false
			}
			if slices.Contains(tags, cg.ManifestTag) {
				return true
			}
			ext := strings.ToLower(filepath.Ext(name))
			_, supported := Detect(name)
			return supported && IsReviewableExtension(ext) && !fileScopeExtensions[ext]
		},
	})
	if err != nil {
		out.Gaps = append(out.Gaps, err.Error())
		return out
	}
	out.Graph, out.Report = result.Graph, result.Report
	for _, diagnostic := range result.InputDiagnostics {
		out.Gaps = append(out.Gaps, diagnostic.Code+": "+diagnostic.Message)
	}
	for name, data := range result.Sources {
		out.Sources[name] = string(data)
	}

	if out.Graph != nil {
		// Project the publication once. Repository ranking does not need to construct
		// a per-file outline or repeatedly scan the full graph's adjacency.
		for _, node := range out.Graph.Nodes() {
			if node.Location == nil || slices.Contains(repocli.SourceRoles(node.Location.Path, nil), "test") {
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
