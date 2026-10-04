package runner

import (
	"context"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/unit"
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
		a.observeGraphBuild(ctx, a.beforeAnalyzer, "before")
		// Repository contracts follow their own snapshot. Current catalog entries
		// must not masquerade as pre-change contracts with the same symbol name.
		catalog := spec.Catalog{}
		git := a.args.GitRunner
		if git == nil {
			git = gitcmd.New(0)
		}
		if data, err := git.Output(ctx, a.args.RepoDir, "show", ref+":.casecodereview/spec.json"); err == nil {
			catalog.Local, _ = spec.Parse(data)
		}
		kinds := spec.KindGates{Spec: a.features.Enabled(feature.SpecCase), Rule: a.features.Enabled(feature.Rule), Link: a.features.Enabled(feature.Link), Doc: a.features.Enabled(feature.Doc)}
		wrap := func(f unit.ClueFinder) unit.ClueFinder { return unit.SnapshotFinder{Finder: f, Snapshot: ref} }
		a.finders = append(a.finders, wrap(spec.NewRelatedFinder(catalog, a.beforeAnalyzer, kinds)))
		if a.features.Enabled(feature.CallerCallee) {
			a.costlyFinders = append(a.costlyFinders, wrap(sourcecontext.CallerFinder{RepoDir: a.args.RepoDir, Index: catalog.Local, Analyzer: a.beforeAnalyzer, Kinds: kinds}), wrap(sourcecontext.CalleeFinder{RepoDir: a.args.RepoDir, Index: catalog.Local, Analyzer: a.beforeAnalyzer, Kinds: kinds}))
		}
	}
	if splitter, ok := a.splitter.(unit.AutoSplitter); ok {
		splitter.Analyzer = a.analyzer
		splitter.Before = a.beforeAnalyzer
		a.splitter = splitter
	}
}
