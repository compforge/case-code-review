package runner

import (
	"context"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/history"
	"github.com/qiankunli/case-code-review/internal/unit/sourcecontext"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

// captureGraphs runs before any graph consumer. Changed bytes come from the
// diff provider; later working-tree edits cannot change their graph identities.
func (a *Runner) captureGraphs(ctx context.Context) {
	if len(a.changes) == 0 {
		return
	}
	after, before := map[string]string{}, map[string]string{}
	needBefore := false
	for _, d := range a.changes {
		if !d.IsDeleted {
			after[d.NewPath] = d.NewFileContent
		}
		if !d.IsNew && d.OldContentKnown {
			before[d.OldPath] = d.OldFileContent
		}
		needBefore = needBefore || d.Deletions > 0 || d.IsDeleted || d.IsRenamed
	}
	if a.analyzer == nil {
		a.analyzer = language.NewSnapshotAnalyzer(a.args.RepoDir, a.changes[0].AfterRef, a.args.GitRunner)
	}
	a.analyzer.Capture(a.changes[0].AfterRef, after)
	if needBefore && a.changes[0].BeforeRef != "" {
		ref := a.changes[0].BeforeRef
		a.beforeAnalyzer = language.NewSnapshotAnalyzer(a.args.RepoDir, ref, a.args.GitRunner)
		a.beforeAnalyzer.SetDocuments(before)
		if a.beforeSource != nil {
			a.beforeAnalyzer.SetSnapshot(a.beforeSource)
		}
		a.observeGraphBuild(ctx, a.beforeAnalyzer, "before")
		// Repository contracts follow their own snapshot. Current catalog entries
		// must not masquerade as pre-change contracts with the same symbol name.
		catalog := spec.Catalog{}
		git := a.args.GitRunner
		if git == nil {
			git = gitcmd.New(0)
		}
		if a.beforeSource != nil {
			if data, err := a.beforeSource.Read(ctx, ".casecodereview/spec.json"); err == nil {
				catalog.Local, _ = spec.Parse([]byte(data))
			}
		} else if data, err := git.Output(ctx, a.args.RepoDir, "show", ref+":.casecodereview/spec.json"); err == nil {
			catalog.Local, _ = spec.Parse(data)
		}
		kinds := spec.KindGates{Spec: a.features.Enabled(feature.SpecCase), Rule: a.features.Enabled(feature.Rule), Link: a.features.Enabled(feature.Link), Doc: a.features.Enabled(feature.Doc)}
		wrap := func(f unit.ClueFinder) unit.ClueFinder { return unit.SnapshotFinder{Finder: f, Snapshot: ref} }
		a.finders = append(a.finders, wrap(spec.NewRelatedFinder(catalog, a.beforeAnalyzer, kinds)))
		if a.features.Enabled(feature.CallerCallee) {
			a.costlyFinders = append(a.costlyFinders, wrap(sourcecontext.CallerFinder{RepoDir: a.args.RepoDir, Index: catalog.Local, Analyzer: a.beforeAnalyzer, Kinds: kinds}), wrap(sourcecontext.CalleeFinder{RepoDir: a.args.RepoDir, Index: catalog.Local, Analyzer: a.beforeAnalyzer, Kinds: kinds}))
		}
	}

}

func (a *Runner) configureFinders(catalog spec.Catalog) {
	f, analyzer := a.features, a.analyzer
	kinds := spec.KindGates{
		Spec: f.Enabled(feature.SpecCase),
		Rule: f.Enabled(feature.Rule),
		Link: f.Enabled(feature.Link),
		Doc:  f.Enabled(feature.Doc),
	}
	finders := []unit.ClueFinder{spec.NewRelatedFinder(catalog, analyzer, kinds)}
	if f.Enabled(feature.History) {
		finders = append(finders, history.Finder{Index: a.args.HistoryIndex})
	}
	// One CodeGraph snapshot per review, shared by clue finders and merge
	// adjacency, owner/used contracts and documentation; built lazily on first use.

	var costlyFinders []unit.ClueFinder
	// caller/callee sit behind the cost gate (graph traversal) and emit per the
	// kind gates: inherited/depended-on specs when the spec kind is on and a spec
	// index exists, direct neighbors' docstrings when the doc kind is on. The two
	// payloads are peer marks (authored vs derived) — doc needs no spec.json, so a
	// repo that never adopted spec-case still gets caller/callee context.
	// Resolution is intra-repo, hence the local index.
	if f.Enabled(feature.CallerCallee) && (kinds.Spec || kinds.Doc) {
		costlyFinders = append(costlyFinders,
			sourcecontext.CallerFinder{RepoDir: a.args.RepoDir, Index: catalog.Local, Kinds: kinds, Analyzer: analyzer},
			sourcecontext.CalleeFinder{RepoDir: a.args.RepoDir, Index: catalog.Local, Kinds: kinds, Analyzer: analyzer},
		)
	}
	a.finders, a.costlyFinders = finders, costlyFinders
}
