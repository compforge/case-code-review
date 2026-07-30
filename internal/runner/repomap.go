package runner

import (
	"context"
	"time"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/codegraph"
)

// buildRepoMap builds the run-level ranked symbol map: seeds are the diff's
// files and the symbols its units cover, so ranking pulls in exactly the
// neighborhood a reviewer would otherwise discover through (often guessed
// and empty) searches. Cost is one syntactic sweep per run, shared by every
// unit — same amortization as background/rule. Best-effort: any weirdness
// degrades to "" and the {{repo_map}} placeholder collapses.
func (a *Runner) buildRepoMap(units []unit.Unit) string {
	start := time.Now()

	var seedFiles []string
	for i := range a.diffs {
		if !a.diffs[i].IsDeleted && a.diffs[i].NewPath != "" {
			seedFiles = append(seedFiles, a.diffs[i].NewPath)
		}
	}
	var seedIdents []string
	seen := map[string]bool{}
	addIdent := func(s string) {
		if s != "" && !seen[s] && codegraph.IsLikelySymbolName(s) {
			seen[s] = true
			seedIdents = append(seedIdents, s)
		}
	}
	for i := range units {
		for _, sid := range units[i].AllSymbols() {
			_, sym, ok := language.SplitSymbolID(sid)
			if !ok {
				continue
			}
			addIdent(sym) // "Recv.Method" or bare func name
			if bare := language.BareName(sym); bare != sym {
				addIdent(bare) // bare method name — call sites use this form
			}
		}
	}

	ex := codegraph.Scan(a.args.RepoDir)
	codegraph.PairMethodIdents(ex)
	m := codegraph.BuildMap(ex, codegraph.MapRequest{
		SeedFiles:  seedFiles,
		SeedIdents: seedIdents,
	})
	if m != "" {
		// No stdout print here: DryRun also calls this, and --format json
		// must stay machine-parseable. The review path logs at its call site.
		telemetry.Event(context.Background(), "repomap.built",
			telemetry.AnyToAttr("seed.files", len(seedFiles)),
			telemetry.AnyToAttr("seed.idents", len(seedIdents)),
			telemetry.AnyToAttr("duration.ms", time.Since(start).Milliseconds()))
	}
	return m
}
