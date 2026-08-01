package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/rules"
	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/config/toolsconfig"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/board"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/runner/formation"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/source"
	"github.com/qiankunli/case-code-review/internal/runner/trial"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"github.com/qiankunli/case-code-review/internal/unit/codegraph"
	"github.com/qiankunli/case-code-review/internal/unit/history"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
	"github.com/qiankunli/go-stdx/slicesx"
)

// Warning is the non-fatal execution warning exposed by Runner.
type Warning = harness.Warning

// Args holds all dependencies and configuration needed to run a review session.
type Args struct {
	// RepoDir is the root of the git repository.
	RepoDir string

	// From and To define the diff range (e.g., "main..feature-branch").
	From string
	To   string

	// Commit is a single commit hash to review (vs its parent).
	Commit string

	// ReviewMode is one of "workspace", "range", or "commit".
	// When empty, it is derived from From/To/Commit at session creation time.
	// Full-scan reviews are owned by internal/runner/scan and never reach this Args.
	ReviewMode string

	// Template loaded from YAML config file.
	Template template.Template

	// SystemRule holds path-based review rules loaded from a JSON config.
	SystemRule rules.Resolver

	// FileFilter holds user-configured include/exclude path patterns from rule.json.
	// When nil, only the default extension and path filters apply.
	FileFilter *rules.FileFilter

	// LLM client for model inference.
	LLMClient llm.LLMClient

	// Tool registry mapping tool aliases to implementations.
	Tools *tool.Registry

	// PlanToolDefs holds llm.ToolDef entries enabled in plan_task, built once at startup.
	// When nil, plan phase sends no tool definitions (same as Java behavior when plan_task is false).
	PlanToolDefs []llm.ToolDef

	// MainToolDefs holds llm.ToolDef entries enabled in main_task, built once at startup.
	MainToolDefs []llm.ToolDef

	// WorkerPool — separate goroutine pool for running asynchronous
	// comment post-processing tasks (tracking, re-tracking, reflection,
	// suggestion validation). This mirrors the Java side's subtaskExecutor
	// which executes the CODE_COMMENT tool off the critical path so that the
	// main LLM tool-use loop can continue issuing requests while comments are
	// being processed in the background.
	//
	// When nil (the default), comment processing happens synchronously inside
	// executeToolCall instead of via a separate worker pool.
	WorkerPool *harness.WorkerPool

	// Concurrency limit for per-file subtasks. Defaults to number of CPUs.
	MaxConcurrency int

	// Concurrent task timeout in minutes. 0 means no timeout.
	ConcurrentTaskTimeout int

	// Findings stores only post-Trial findings. Investigative hypotheses use a
	// separate Runner-owned collector and cannot leak through this boundary.
	Findings *finding.Collector

	// Background is an optional requirement/business context string
	// injected into plan and main_task prompts via {{requirement_background}}.
	Background string

	// BizID is an opaque caller-owned identity persisted for observability.
	// It never changes review behavior or enters model prompts.
	BizID string

	// Specs is the loaded spec knowledge: this repo's entries by symbol-id plus
	// dependency entries by fqn (two address spaces — see spec.Catalog). A review
	// Unit's related symbols are looked up here and injected as the contract
	// checklist via {{spec_cases}}. Zero value when no spec is configured.
	Specs spec.Catalog

	// HistoryIndex is the loaded --history (symbol-id -> prior findings). A unit's
	// covered functions are looked up here and injected via {{prior_findings}} so
	// the reviewer reconciles them with the change. Nil when no history is passed.
	HistoryIndex history.Index

	// Model is the user-configured model name used as fallback when
	// template phases (plan/memory_compression) don't specify one.
	Model string

	// GitRunner limits the total number of concurrent git subprocesses.
	// When nil, subprocesses are spawned without a global limit.
	GitRunner *gitcmd.Runner

	// Session is an optional session history instance for collecting conversation records.
	// When nil, a default one is created automatically with git branch auto-detected from repoDir.
	Session *session.SessionHistory

	// Features are the resolved feature gates for this run (ablation toggles). Nil
	// means all defaults (full feature set) — so callers that don't set it are
	// unaffected. Gates flow into unit splitting, clue finders, and review phases.
	Features feature.Set

	// Version is the ccr build identity ("v1.7.1 (dc030bd)"), stamped into the
	// session manifest so transcripts self-describe which build produced them.
	Version string
}

// Runner orchestrates the AI-powered code review. Harness owns one Unit's
// generic agent loop; this struct owns Unit formation, review extensions and
// run-level aggregation.
type Runner struct {
	args             Args
	changes          []change.Change // parsed diffs
	fileSelections   map[string]fileSelection
	componentClues   map[string][]unit.Clue
	contextFileCount int
	totalInsertions  int64
	totalDeletions   int64
	currentDate      string
	session          *session.SessionHistory
	unitFailed       int64 // count of failed unit reviews, accessed atomically
	executor         *unitreview.Executor
	// splitter turns each changed file's diff into diff units (one per changed
	// function); merger consolidates those into review units (what actually
	// triggers a review loop). Both set in New().
	splitter unit.Splitter
	merger   unit.Merger
	// finders fill each diff unit's Clues before merge. Cheap finders (spec.json
	// lookups) always run; costly ones (call-graph grep) run only when the diff
	// is focused enough that units won't coalesce — see splitUnits.
	finders       []unit.ClueFinder
	costlyFinders []unit.ClueFinder
	// features are the resolved ablation gates; consulted in splitUnits (callchain)
	// and dispatchUnits (plan / hypothesis review). Clue gates are applied at finder
	// assembly in New(), so findClues stays gate-agnostic.
	features feature.Set
	// costlyContext records splitUnits' budget-gate verdict (diff focused enough
	// for call-graph greps) so the initial context's usage-sites grep — same cost class —
	// rides the same gate. Set in splitUnits, read by renderUsageSites.
	costlyContext bool
	// repoMap is the run-level ranked symbol map (computed once in
	// dispatchUnits, shared by every unit's prompt — like background/rule).
	// It exists to stop the reviewer from guessing symbol names in searches:
	// it lists names that actually exist, ranked by relevance to the diff.
	repoMap string
	// typedGraph is the shared lazy handle to the typed Go call graph
	// (nil when the typed_graph gate is off). See codegraph.TypedGraph.
	typedGraph *codegraph.TypedGraph
	// analyzer is the run-scoped source-language boundary shared by splitting,
	// callgraph lookup, comment tagging, and ranged source preload.
	analyzer *language.Analyzer
	// board is the Review Team's shared case board for this run (nil when the
	// review_team gate is off). See docs/unit_review.md.
	board *board.Registry
	// hypotheses contains the divergent Unit Review output. It is separate from
	// args.Findings so unassessed suspicions can never leak into public results.
	hypotheses     *unitreview.Collector
	hypothesisHook *unitreview.HypothesisHook
}

// sourceAnalyzer preserves the useful zero-value shape of Runner in focused
// tests while New keeps the production path on one run-scoped cache.
func (a *Runner) sourceAnalyzer() *language.Analyzer {
	if a.analyzer != nil {
		return a.analyzer
	}
	return language.NewAnalyzer(a.args.RepoDir)
}

// New creates a new Runner from the given arguments.
func New(args Args) *Runner {
	if args.Tools == nil {
		args.Tools = tool.NewRegistry()
	}
	if args.Findings == nil {
		args.Findings = finding.NewCollector()
	}
	if args.Session == nil {
		gitBranch := detectGitBranch(context.Background(), args.RepoDir)
		mode := args.ReviewMode
		if mode == "" {
			mode = reviewModeString(args.From, args.To, args.Commit)
		}
		args.Session = session.New(args.RepoDir, gitBranch, args.Model, session.SessionOptions{
			ReviewMode: mode,
			DiffFrom:   args.From,
			DiffTo:     args.To,
			DiffCommit: args.Commit,
			BizID:      args.BizID,
			// Run manifest: transcripts self-describe their configuration so eval
			// joins on gates/version/knobs instead of guessing.
			Features:    args.Features.Resolved(),
			ToolVersion: args.Version,
			Params: map[string]any{
				"unit_watermark":       formation.DefaultWatermark,
				"preload_budget_bytes": preloadSourceBudget,
			},
			GitHead: detectGitHead(context.Background(), args.RepoDir),
		})
	}
	// Clue gates are applied here (finder assembly) rather than in findClues, so a
	// disabled clue kind simply never fires. Gates are two orthogonal axes: KIND
	// gates (spec_case/rule/link/doc) switch an evidence kind off across every
	// relation — the ablation unit matches the dry-run relation×kind matrix — and
	// the caller_callee COST gate switches the expensive call-graph walk. Relations
	// themselves are not gated (cheap mechanism). See docs/unit-model.md.
	f := args.Features
	analyzer := language.NewAnalyzer(args.RepoDir)
	kinds := spec.KindGates{
		Spec: f.Enabled(feature.SpecCase),
		Rule: f.Enabled(feature.Rule),
		Link: f.Enabled(feature.Link),
		Doc:  f.Enabled(feature.Doc),
	}
	finders := []unit.ClueFinder{spec.NewRelatedFinder(args.Specs, args.RepoDir, kinds)}
	if f.Enabled(feature.History) {
		finders = append(finders, history.Finder{Index: args.HistoryIndex})
	}
	// One typed call graph per review, shared by clue finders and merge
	// adjacency; lazily built on first Go neighbor query. Gate off -> nil
	// handle -> every consumer stays on the grep heuristics.
	var typed *codegraph.TypedGraph
	if f.Enabled(feature.TypedGraph) {
		typed = &codegraph.TypedGraph{RepoDir: args.RepoDir}
	}
	var costlyFinders []unit.ClueFinder
	// caller/callee sit behind the cost gate (call-graph grep) and emit per the
	// kind gates: inherited/depended-on specs when the spec kind is on and a spec
	// index exists, direct neighbors' docstrings when the doc kind is on. The two
	// payloads are peer marks (authored vs derived) — doc needs no spec.json, so a
	// repo that never adopted spec-case still gets caller/callee context.
	// Resolution is intra-repo, hence the local index.
	if f.Enabled(feature.CallerCallee) && (kinds.Spec || kinds.Doc) {
		costlyFinders = append(costlyFinders,
			codegraph.CallerFinder{RepoDir: args.RepoDir, Index: args.Specs.Local, Runner: args.GitRunner, Kinds: kinds, Typed: typed, Analyzer: analyzer},
			codegraph.CalleeFinder{RepoDir: args.RepoDir, Index: args.Specs.Local, Runner: args.GitRunner, Kinds: kinds, Typed: typed, Analyzer: analyzer},
		)
	}
	a := &Runner{
		args:     args,
		session:  args.Session,
		features: f,
		// AutoSplitter cuts each file to function-level diff units by language,
		// degrading to file scope when its parser is unavailable;
		// WatermarkMerger coalesces them into review units above the watermark.
		splitter:      unit.AutoSplitter{RepoDir: args.RepoDir, Analyzer: analyzer},
		merger:        unit.WatermarkMerger{Watermark: formation.DefaultWatermark},
		finders:       finders,       // cheap spec.json / history clues, gated per kind
		costlyFinders: costlyFinders, // call-graph caller/callee clues (gated + budget-gated)
		typedGraph:    typed,
		analyzer:      analyzer,
	}
	// Review Team board (docs/unit_review.md): one shared in-memory board per run
	// when the gate is on; nil otherwise (loop behavior byte-identical).
	if f.Enabled(feature.ReviewTeam) {
		a.board = board.New()
	}
	hypotheses := unitreview.NewCollector()
	hypothesisHook := &unitreview.HypothesisHook{
		Collector:    hypotheses,
		WorkerPool:   args.WorkerPool,
		Session:      args.Session,
		ChangeLookup: a.findChange,
		LLMClient:    args.LLMClient,
		Template:     args.Template,
		Model:        args.Model,
		Relocation:   f.Enabled(feature.Relocation),
	}
	a.hypotheses = hypotheses
	a.hypothesisHook = hypothesisHook
	compressionSystemPrompt, compressionPrompt := reviewCompressionPrompts(args)
	a.executor = unitreview.NewExecutor(unitreview.ExecutorConfig{
		LLMClient:               args.LLMClient,
		Model:                   args.Model,
		Tools:                   args.Tools,
		ToolDefs:                args.MainToolDefs,
		Session:                 args.Session,
		MaxTurns:                args.Template.MaxToolRequestTimes,
		MaxTokens:               args.Template.MaxTokens,
		FileDedup:               f.Enabled(feature.FileDedup),
		FileEvict:               f.Enabled(feature.FileEvict),
		PostBulletin:            f.Enabled(feature.PostBulletin),
		CompressionSystemPrompt: compressionSystemPrompt,
		CompressionPrompt:       compressionPrompt,
	}, hypothesisHook, a.board)
	hypothesisHook.RecordUsage = a.executor.RecordUsage
	return a
}

// Run executes the full review pipeline: parse diffs -> plan per file -> LLM tool-loop -> collect comments.
func (a *Runner) Run(ctx context.Context) ([]finding.Finding, error) {
	// Mirror this run's [ccr] warnings/errors into a log next to the session's
	// JSONL transcript, so they survive a detached/background run where the
	// terminal's stderr is gone. Best-effort: a failure here never blocks review.
	if a.session != nil {
		if logPath, err := a.session.LogPath(); err == nil {
			if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
				restore := console.AddErrSink(f)
				defer func() { restore(); f.Close() }()
			}
		}
	}

	// Step 1: Parse diffs
	ctx, diffSpan := telemetry.StartSpan(ctx, "diff.parse")
	if err := a.loadChanges(ctx); err != nil {
		diffSpan.End()
		return nil, fmt.Errorf("load diffs: %w", err)
	}
	a.prepareFileSelections(ctx)
	telemetry.SetAttr(diffSpan, "files.changed", len(a.changes))
	telemetry.SetAttr(diffSpan, "lines.inserted", int64(a.totalInsertions))
	telemetry.SetAttr(diffSpan, "lines.deleted", int64(a.totalDeletions))
	diffSpan.End()

	// Build the read-only DiffMap from ALL parsed diffs (before filtering)
	// so the LLM can query diffs of related but filtered-out files.
	a.injectDiffMap()
	a.args.Tools.Freeze()

	totalChanged := len(a.changes)
	reviewCount := a.countReviewable(a.changes)
	fmt.Fprintf(console.Out(), "[ccr] %d file(s) changed, reviewing %d with %d project context file(s) in %s\n",
		totalChanged, reviewCount, a.contextFileCount, a.args.RepoDir)

	a.changes = a.filterDiffs(a.changes)

	if len(a.changes) == 0 {
		if a.contextFileCount > 0 {
			fmt.Fprintf(console.Out(), "[ccr] No Unit Review targets; %d project context file(s) changed. Skipping review.\n", a.contextFileCount)
		} else {
			fmt.Fprintln(console.Out(), "[ccr] No supported files changed. Skipping review.")
		}
		telemetry.Event(ctx, "no.files.changed")
		a.session.Finalize()
		return []finding.Finding{}, nil
	}

	a.currentDate = time.Now().Format("2006-01-02 15:04")
	telemetry.Event(ctx, "review.started",
		telemetry.AnyToAttr("file.count", totalChanged),
		telemetry.AnyToAttr("review.count", reviewCount),
		telemetry.AnyToAttr("repo.dir", a.args.RepoDir))

	// Record file count metric.
	telemetry.RecordFilesReviewed(ctx, int64(reviewCount))

	// Step 2: Dispatch per-unit reviews concurrently
	comments, err := a.dispatchUnits(ctx)
	if len(comments) > 0 {
		telemetry.RecordCommentsGenerated(ctx, int64(len(comments)))
	}
	a.session.Finalize()
	return comments, err
}

// Session returns the session history associated with this Runner.
func (a *Runner) Session() *session.SessionHistory {
	return a.session
}

// FilesReviewed returns the number of changed files included in this review.
func (a *Runner) FilesReviewed() int64 {
	return int64(len(a.changes))
}

// Changes returns the file changes loaded by the Runner source.
func (a *Runner) Changes() []change.Change {
	return a.changes
}

// TotalTokensUsed returns PromptTokens + CompletionTokens across all LLM calls.
// For Anthropic, PromptTokens already includes cache read/write tokens.
func (a *Runner) TotalTokensUsed() int64 { return a.executor.TotalTokensUsed() }

// TotalInputTokens returns the accumulated input/prompt tokens from all LLM calls.
func (a *Runner) TotalInputTokens() int64 { return a.executor.TotalInputTokens() }

// TotalOutputTokens returns the accumulated completion tokens from all LLM calls.
func (a *Runner) TotalOutputTokens() int64 { return a.executor.TotalOutputTokens() }

// TotalCacheReadTokens returns the accumulated cache read tokens from all LLM calls.
func (a *Runner) TotalCacheReadTokens() int64 { return a.executor.TotalCacheReadTokens() }

// TotalCacheWriteTokens returns the accumulated cache write tokens from all LLM calls.
func (a *Runner) TotalCacheWriteTokens() int64 { return a.executor.TotalCacheWriteTokens() }

// ProjectSummary returns the markdown project-level summary. Always empty
// for the diff-review path; defined so *Runner satisfies the
// cmd/ccr.ResultProvider interface that scan.Runner also implements.
func (a *Runner) ProjectSummary() string { return "" }

// Warnings returns a copy of non-fatal warnings recorded during review.
func (a *Runner) Warnings() []Warning { return a.executor.Warnings() }

// ToolCalls returns per-tool call counts accumulated during review.
func (a *Runner) ToolCalls() map[string]int64 { return a.executor.ToolCalls() }

// ModelsUsed returns the routing aliases that served a response this review,
// each with its response count (deduped). Empty for a single-model run.
func (a *Runner) ModelsUsed() map[string]int { return a.executor.ModelsUsed() }

// recordWarning adds a non-fatal warning to the agent's warning list.
func (a *Runner) recordWarning(warningType, file, message string) {
	a.executor.RecordWarning(warningType, file, message)
}

// loadChanges populates the diff-related fields.
func (a *Runner) loadChanges(ctx context.Context) error {
	var provider *source.Provider

	switch {
	case a.args.Commit != "":
		provider = source.NewCommitProvider(a.args.RepoDir, a.args.Commit, a.args.GitRunner)
	case a.args.From != "" && a.args.To != "":
		provider = source.NewProvider(a.args.RepoDir, a.args.From, a.args.To, a.args.GitRunner)
	default:
		provider = source.NewWorkspaceProvider(a.args.RepoDir, a.args.GitRunner)
	}

	parsed, err := provider.GetDiff(ctx)
	if err != nil {
		return fmt.Errorf("get diffs: %w", err)
	}

	a.changes = parsed
	if p, ok := a.args.Tools.Get(tool.FileReadBase.Name()); ok {
		if base, ok := p.(*tool.FileReadProvider); ok {
			base.SetRef(provider.BaseRef(ctx))
		}
	}

	for i := range parsed {
		d := &parsed[i]
		a.totalInsertions += d.Insertions
		a.totalDeletions += d.Deletions
	}
	// Diff size lands in session_end — the denominator cost metrics normalize by.
	a.session.SetDiffStats(len(parsed), a.totalInsertions, a.totalDeletions)

	return nil
}

// injectDiffMap builds a read-only DiffMap from parsed diffs and sets it
// on the FileReadDiffProvider, plus the rename/delete path map on the
// FileReadProvider (so a read of a path this diff moved or removed gets
// redirected/explained instead of a raw git miss). Must be called after
// loadChanges and before any concurrent access to the registry.
func (a *Runner) injectDiffMap() {
	m := make(map[string]string, len(a.changes))
	renamedTo := make(map[string]string)
	deleted := make(map[string]bool)
	for i := range a.changes {
		d := &a.changes[i]
		if d.NewPath != "/dev/null" {
			m[d.NewPath] = d.Diff
		}
		if d.IsRenamed && d.OldPath != "" && d.NewPath != "" && d.OldPath != d.NewPath {
			renamedTo[d.OldPath] = d.NewPath
		}
		if d.IsDeleted && d.OldPath != "" && d.OldPath != "/dev/null" {
			deleted[d.OldPath] = true
		}
	}
	dm := tool.NewDiffMap(m)
	if p, ok := a.args.Tools.Get(tool.FileReadDiff.Name()); ok {
		if frd, ok := p.(*tool.FileReadDiffProvider); ok {
			frd.SetDiffMap(dm)
		}
	}
	if p, ok := a.args.Tools.Get(tool.FileRead.Name()); ok {
		if frp, ok := p.(*tool.FileReadProvider); ok {
			frp.SetDiffPaths(tool.NewDiffPaths(renamedTo, deleted))
		}
	}
}

// dispatchUnits runs the Plan + Main phases for each review Unit concurrently.
func (a *Runner) dispatchUnits(ctx context.Context) ([]finding.Finding, error) {
	startTime := time.Now()
	defer func() {
		telemetry.RecordReviewDuration(ctx, time.Since(startTime))
	}()

	// Pre-filter: discard diffs whose diff content alone exceeds 80% of the token threshold.
	a.changes = a.filterLargeDiffs(a.changes)
	if len(a.changes) == 0 {
		return nil, fmt.Errorf("all diffs filtered out by token size")
	}

	// Split each surviving (non-deleted) file diff into review Units (function-
	// level for Go, file-level otherwise / when coarsened by the cost governor).
	// The fan-out machinery below is granularity-agnostic.
	units, err := a.splitUnits()
	if err != nil {
		return nil, err
	}

	if a.features.Enabled(feature.RepoMap) {
		a.repoMap = a.buildRepoMap(units)
		if a.repoMap != "" {
			fmt.Fprintf(console.Out(), "[ccr] Repo map built (~%d tokens) — injected into every unit\n", len(a.repoMap)/4)
		}
	}

	var dossierCoordinator *dossierCoordinator
	if a.features.Enabled(feature.HypothesisReview) {
		task := a.args.Template.HypothesisReviewTask
		if task != nil && len(task.Messages) > 0 {
			dossierCoordinator = newDossierCoordinator(dossierCoordinatorConfig{
				Context: ctx, Units: units, Changes: a.changes, Selections: a.fileSelections,
				QuietWindow: dossierQuietWindow, MaxWait: dossierMaxWait,
				MaxHypotheses: dossierMaxHypotheses, Concurrency: dossierReviewWorkers,
				ReadPaths: a.executor.ReadPaths, Review: a.reviewDossier,
				OnHypothesis: a.persistHypothesis, OnSealed: a.persistDossier,
			})
			a.hypothesisHook.OnResolved = dossierCoordinator.Submit
		}
	}

	var wg sync.WaitGroup

	concurrency := a.args.MaxConcurrency
	if concurrency <= 0 {
		concurrency = 8
	}

	sem := make(chan struct{}, concurrency)
	timeout := time.Duration(a.args.ConcurrentTaskTimeout) * time.Minute

	var dispatched int64
	for i := range units {
		dispatched++
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore

		go func(u unit.Unit) {
			defer wg.Done()
			defer func() { <-sem }() // release
			// A panic while reviewing one unit must be isolated exactly like an
			// error return: counted in unitFailed and recorded as a unit_error
			// warning, so other units still complete and the all-failed rollup
			// below stays correct. Registered before the timeout-cancel defer,
			// so cancel() still runs first on unwind and fileCtx is already
			// cancelled here — use the parent ctx for telemetry.
			defer func() {
				if r := recover(); r != nil {
					atomic.AddInt64(&a.unitFailed, 1)
					fmt.Fprintf(console.Out(), "[ccr] Unit review panic for %s: %v\n%s\n", u.ID, r, debug.Stack())
					telemetry.ErrorEvent(ctx, "unit.panic", fmt.Errorf("panic: %v", r),
						telemetry.AnyToAttr("file.path", u.Path()))
					a.recordWarning("unit_error", u.Path(), fmt.Sprintf("panic: %v", r))
					// The unit never reached its own Close — end the lifecycle
					// here so the panic is visible in debriefs, not just logs.
					a.session.CloseScope(
						session.Scope{ID: u.ID, Kind: "unit", Type: string(u.Scope), Paths: u.Paths()},
						session.Debrief{Formed: string(u.Formed), Outcome: "panic", Reason: fmt.Sprintf("%v", r)})
				}
			}()

			var fileCtx context.Context
			var cancel context.CancelFunc
			if timeout > 0 {
				fileCtx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			} else {
				fileCtx = ctx
			}

			if err := a.reviewUnit(fileCtx, u); err != nil {
				atomic.AddInt64(&a.unitFailed, 1)
				fmt.Fprintf(console.Out(), "[ccr] Unit review error for %s: %v\n", u.ID, err)
				telemetry.ErrorEvent(fileCtx, "unit.error", err,
					telemetry.AnyToAttr("file.path", u.Path()))
				a.recordWarning("unit_error", u.Path(), err.Error())
			}
		}(units[i])
	}

	wg.Wait()

	if dispatched == 0 {
		if dossierCoordinator != nil {
			dossierCoordinator.Finish()
		}
		return []finding.Finding{}, nil
	}

	// All subtasks finished — collect comments from the global collector once.
	if a.args.WorkerPool != nil {
		a.args.WorkerPool.Await()
	}

	var hypotheses []unitreview.Hypothesis
	var assessments []hypothesisreview.Assessment
	if dossierCoordinator != nil {
		hypotheses, assessments = dossierCoordinator.Finish()
	} else {
		hypotheses = a.hypotheses.Hypotheses()
		a.persistHypotheses(hypotheses)
	}

	failed := atomic.LoadInt64(&a.unitFailed)
	if failed > 0 && failed == dispatched {
		return nil, fmt.Errorf("all %d unit review(s) failed — check your LLM configuration and API key", dispatched)
	}

	var comments []finding.Finding
	if a.features.Enabled(feature.HypothesisReview) {
		if dossierCoordinator == nil && len(hypotheses) > 0 {
			a.recordWarning(
				"hypothesis_review_unavailable", "",
				"hypotheses cannot pass Trial because HYPOTHESIS_REVIEW_TASK is not configured",
			)
		}
		comments = trial.Run(hypotheses, assessments)
	} else {
		// Gate-off is the one-stage baseline used by eval ablations.
		comments = trial.Bypass(hypotheses)
	}
	a.persistTrialDecisions(hypotheses, assessments)
	for _, comment := range comments {
		a.args.Findings.Add(comment)
	}
	a.tagSymbolIDs(comments)
	tagFingerprints(comments)
	a.persistFindings(comments)
	a.persistBoardPosts()
	return comments, nil
}

// persistBoardPosts drains the run's board bulletins into the transcript
// (board_post records) — the in-memory board's attribution/replay trail.
func (a *Runner) persistBoardPosts() {
	if a.board == nil {
		return
	}
	posted := a.board.Posted()
	if len(posted) == 0 {
		return
	}
	out := make([]session.BoardPost, 0, len(posted))
	for _, b := range posted {
		out = append(out, session.BoardPost{
			From: b.From, Turn: b.Turn, Level: int(b.Level),
			Paths: b.Paths, Symbols: b.Symbols, Text: b.Text,
		})
	}
	a.session.WriteBoardPosts(out)
}

// tagFingerprints stamps each comment's stable identity: sha256(path\0content),
// 12 hex chars. Lines are deliberately excluded — relocation and later edits
// shift them, and the fingerprint's job is joining human labels and posterior
// evidence to "the same finding" across re-runs.
func tagFingerprints(comments []finding.Finding) {
	for i := range comments {
		h := sha256.Sum256([]byte(comments[i].Path + "\x00" + comments[i].Content))
		comments[i].Fingerprint = hex.EncodeToString(h[:])[:12]
	}
}

// persistFindings writes the run's delivered post-Trial findings into the
// session transcript, so eval never mistakes investigative tool calls for
// public results.
func (a *Runner) persistFindings(comments []finding.Finding) {
	findings := make([]session.Finding, 0, len(comments))
	for _, c := range comments {
		findings = append(findings, session.Finding{
			HypothesisID: c.HypothesisID,
			Path:         c.Path,
			StartLine:    c.StartLine,
			EndLine:      c.EndLine,
			SymbolID:     c.SymbolID,
			Fingerprint:  c.Fingerprint,
			Alias:        c.Alias,
			Content:      c.Content,
			Category:     c.Category,
			Severity:     c.Severity,
		})
	}
	a.session.WriteFindings(findings)
}

func (a *Runner) persistHypotheses(hypotheses []unitreview.Hypothesis) {
	for _, h := range hypotheses {
		a.persistHypothesis(h)
	}
}

func (a *Runner) persistHypothesis(h unitreview.Hypothesis) {
	a.session.WriteArtifact("review_hypothesis", map[string]any{
		"id": h.ID, "origin_unit": h.OriginUnit, "path": h.Path,
		"content": h.Content, "existing_code": h.ExistingCode,
		"start_line": h.StartLine, "end_line": h.EndLine,
		"trigger": h.Trigger, "impact": h.Impact,
		"change_attribution": h.ChangeAttribution,
		"evidence":           h.Evidence, "uncertainty": h.Uncertainty,
		"alias": h.Alias, "category": h.Category, "severity": h.Severity,
	})
}

func (a *Runner) persistTrialDecisions(
	hypotheses []unitreview.Hypothesis,
	assessments []hypothesisreview.Assessment,
) {
	byID := make(map[string]unitreview.Hypothesis, len(hypotheses))
	for _, hypothesis := range hypotheses {
		byID[hypothesis.ID] = hypothesis
	}
	for _, assessment := range assessments {
		hypothesis, ok := byID[assessment.HypothesisID]
		a.session.WriteArtifact("trial_decision", map[string]any{
			"dossier_id":                  assessment.DossierID,
			"assessment_submission_index": assessment.SubmissionIndex,
			"hypothesis_id":               assessment.HypothesisID,
			"passed_trial":                ok && trial.Passes(hypothesis, assessment),
		})
	}
}

// tagSymbolIDs resolves each comment's enclosing symbol-id (<relpath>::<symbol>)
// from the post-change file, so callers (e.g. devloop) can key review history by
// unit instead of by drift-prone line numbers. Best-effort: a comment whose line
// resolves to no function — or an unsupported file — is left untagged.
func (a *Runner) tagSymbolIDs(comments []finding.Finding) {
	for i := range comments {
		d := a.findChange(comments[i].Path)
		if d == nil || d.NewFileContent == "" {
			continue
		}
		if definition, ok := a.sourceAnalyzer().DefinitionAt(context.Background(), language.Source{
			Path: comments[i].Path, Content: d.NewFileContent,
		}, comments[i].StartLine); ok {
			comments[i].SymbolID = definition.SymbolID
		}
	}
}

// splitUnits delegates Unit creation to Formation, then registers the stable
// scopes with the optional run-level Review Team board.
func (a *Runner) splitUnits() ([]unit.Unit, error) {
	finders := append([]unit.ClueFinder(nil), a.finders...)
	finders = append(finders, componentFinder{
		selections: a.fileSelections,
		clues:      a.componentClues,
	})
	units, costly, err := formation.Form(formation.Config{
		RepoDir:       a.args.RepoDir,
		Changes:       a.changes,
		Splitter:      a.splitter,
		Merger:        a.merger,
		Finders:       finders,
		CostlyFinders: a.costlyFinders,
		GitRunner:     a.args.GitRunner,
		TypedGraph:    a.typedGraph,
		CallChain:     a.features.Enabled(feature.CallChain),
	})
	if err != nil {
		return nil, err
	}
	a.costlyContext = costly

	// Review Team: register each unit's board interest before dispatch — its
	// files + covered symbols + clue neighbors (the Relation axis reused as
	// "what this unit watches"; see docs/unit_review.md).
	if a.board != nil {
		for _, u := range units {
			a.board.Register(u.ID, unitInterest(u))
		}
	}
	return units, nil
}

// unitInterest builds a unit's board routing filter from its identity (paths +
// covered symbols) and its clue neighbors (caller/callee/owner/used refs).
func unitInterest(u unit.Unit) board.Interest {
	in := board.Interest{Paths: map[string]bool{}, Symbols: map[string]bool{}}
	for _, p := range u.Paths() {
		in.Paths[p] = true
	}
	for _, s := range u.AllSymbols() {
		in.Symbols[s] = true
	}
	for _, clue := range u.Clues {
		if clue.Ref == "" {
			continue
		}
		if clue.Relation == unit.RelProject {
			in.Paths[clue.Ref] = true
			continue
		}
		in.Symbols[clue.Ref] = true
		if path, _, ok := language.SplitSymbolID(clue.Ref); ok {
			in.Paths[path] = true
		}
	}
	return in
}

// renderClues groups a Unit's Clues into prompt context blocks by Kind: the
// spec/case contract + docstrings ({{spec_cases}}), the @rule criteria, the see-also
// links ({{see_also}}), and a previous review's findings ({{prior_findings}}). Clue
// Text is raw content; how it reached the unit is worded here via clueLabel — one
// place to change, and dedup upstream compares raw content. The rule.json text is
// merged with rules by the caller.
func renderClues(clues []unit.Clue) (specCases, rules, seeAlso, priorFindings string) {
	var specBlocks, ruleLines, linkLines, historyBlocks []string
	for _, c := range clues {
		switch c.Kind {
		case unit.ClueSpec, unit.ClueDoc:
			// Contracts/context the change must respect: own spec, the enclosing
			// type's spec, the caller's governing spec, callee contracts, and
			// referenced-type docstrings — differentiated by the relation label.
			specBlocks = append(specBlocks, clueLabel(c)+c.Text)
		case unit.ClueRule:
			ruleLines = append(ruleLines, "- "+clueLabel(c)+c.Text)
		case unit.ClueLink:
			linkLines = append(linkLines, "- "+c.Text)
		case unit.ClueHistory:
			// Prior-review findings: not a contract — the reviewer reconciles them
			// (already framed as an adjudication task by the history finder).
			historyBlocks = append(historyBlocks, c.Text)
		case unit.ClueProject:
			// Project context has its own prompt section; it is neither a
			// governing contract nor a review target.
			continue
		default:
			// A new ClueKind added without a case here surfaces as context rather
			// than being silently dropped.
			specBlocks = append(specBlocks, clueLabel(c)+c.Text)
		}
	}
	return strings.Join(specBlocks, "\n"), strings.Join(ruleLines, "\n"), strings.Join(linkLines, "\n"), strings.Join(historyBlocks, "\n")
}

func renderProjectContext(clues []unit.Clue) string {
	var lines []string
	for _, clue := range clues {
		if clue.Kind == unit.ClueProject {
			lines = append(lines, "- "+clueLabel(clue)+clue.Text)
		}
	}
	return strings.Join(lines, "\n")
}

// clueLabel words how a clue reached the unit, from (relation, kind, ref) — the
// relation×kind label table. self needs no label (it IS the changed symbol), and
// a self/owner spec none either (Index.Render embeds the symbol-id).
func clueLabel(c unit.Clue) string {
	switch c.Relation {
	case unit.RelOwner:
		name := c.Ref
		if symbol, ok := language.SymbolName(name); ok {
			name = symbol // display the bare type name, not the whole symbol-id
		}
		switch c.Kind {
		case unit.ClueDoc:
			return "enclosing type `" + name + "` (docstring): "
		case unit.ClueSpec:
			return "" // Render output already carries the symbol-id
		default:
			return "(enclosing type `" + name + "`) "
		}
	case unit.RelUsed:
		if c.Kind == unit.ClueDoc {
			return "used type `" + c.Ref + "` (docstring): "
		}
		return "(used type `" + c.Ref + "`) "
	case unit.RelCaller:
		if c.Kind == unit.ClueDoc {
			return "caller `" + c.Ref + "` (docstring): "
		}
		return "(governing spec inherited from caller " + c.Ref + ")\n"
	case unit.RelCallee:
		if c.Kind == unit.ClueDoc {
			return "callee `" + c.Ref + "` (docstring): "
		}
		return "(depends on callee " + c.Ref + ", which guarantees)\n"
	case unit.RelProject:
		return "(same Component project context `" + c.Ref + "`) "
	}
	return ""
}

// reviewUnit performs the Plan Phase + Main Loop for a single review Unit.
func (a *Runner) reviewUnit(ctx context.Context, u unit.Unit) error {
	ctx, span := telemetry.StartSpan(ctx, "unit.review."+u.ID)
	defer span.End()
	telemetry.SetAttr(span, "file.path", u.Path())
	telemetry.SetAttr(span, "lines.changed", u.Insertions()+u.Deletions())
	telemetry.SetAttr(span, "lines.inserted", u.Insertions())
	telemetry.SetAttr(span, "lines.deleted", u.Deletions())

	if ctx.Err() != nil {
		return ctx.Err()
	}

	newPath := u.Path()
	// All of this Unit's tasks (plan / main / compression / relocation) record
	// under one scope keyed by Unit.ID, so the viewer groups them together and
	// a cross-file Unit stays whole.
	sc := session.Scope{ID: u.ID, Kind: "unit", Type: string(u.Scope), Paths: u.Paths()}

	// Build change-files list excluding this Unit's own file(s) — all member paths
	// for a cross-file call-chain Unit, the single path otherwise.
	changeFilesExcludingCurrent := a.buildChangeFilesExcept(u.Paths()...)

	// Render this unit's found context (clues) into the prompt blocks.
	specCases, specRules, seeAlso, priorFindings := renderClues(u.Clues)
	projectContext := renderProjectContext(u.Clues)
	// Pre-grep where else the repo references the changed symbols ({{usage_sites}}).
	usageSites, usageCount := a.renderUsageSites(u)
	// Per-function @rule (from clues) augments the path-glob rule.json criteria;
	// both flow into {{system_rule}} (plan + main).
	rule := a.resolveSystemRule(strings.ToLower(newPath))
	if specRules != "" {
		if rule != "" {
			rule += "\n"
		}
		rule += specRules
	}

	threshold := a.args.Template.PlanModeLineThreshold
	changeLines := u.Insertions() + u.Deletions()

	// Phase 1: Plan (gated off, or skipped when changes are below threshold)
	var planResult string
	planOn := a.features.Enabled(feature.Plan)
	if planOn && a.args.Template.PlanTask != nil && len(a.args.Template.PlanTask.Messages) > 0 && threshold > 0 && changeLines < int64(threshold) {
		fmt.Fprintf(console.Out(), "[ccr] Skipping plan phase for %s (%d lines < threshold %d)\n", newPath, changeLines, threshold)
		telemetry.Event(ctx, "plan.skipped",
			telemetry.AnyToAttr("file.path", newPath),
			telemetry.AnyToAttr("lines.changed", changeLines),
			telemetry.AnyToAttr("threshold", threshold))
	} else if planOn && a.args.Template.PlanTask != nil && len(a.args.Template.PlanTask.Messages) > 0 {
		var err error
		planResult, err = a.executePlanPhase(ctx, sc, u.Diff(), changeFilesExcludingCurrent, rule)
		if err != nil {
			fmt.Fprintf(console.Out(), "[ccr] Plan phase failed for %s: %v (continuing without plan)\n", newPath, err)
			telemetry.Eventf(ctx, "plan.failed", err.Error(),
				telemetry.AnyToAttr("file.path", newPath))
			planResult = ""
		}
	}

	// Phase 2: Main task loop
	if len(a.args.Template.MainTask.Messages) == 0 {
		return fmt.Errorf("main_task.messages is empty in template")
	}

	rawMsgs := a.args.Template.MainTask.Messages
	buildMessages := func(unitSource, relatedSource string) []llm.Message {
		messages := make([]llm.Message, 0, len(rawMsgs))
		for _, m := range rawMsgs {
			content := m.Content
			content = strings.ReplaceAll(content, "{{current_system_date_time}}", a.currentDate)
			content = strings.ReplaceAll(content, "{{current_file_path}}", newPath)
			content = strings.ReplaceAll(content, "{{system_rule}}", rule)
			content = strings.ReplaceAll(content, "{{change_files}}", changeFilesExcludingCurrent)
			content = strings.ReplaceAll(content, "{{diff}}", u.Diff())
			// High-confidence source already implied by the Unit is appended as
			// separate File messages so early rounds do not fetch it again.
			content = strings.ReplaceAll(content, "{{unit_source}}", unitSource)
			content = strings.ReplaceAll(content, "{{related_source}}", relatedSource)
			// Pre-grepped blast-radius map of the changed symbols.
			content = strings.ReplaceAll(content, "{{usage_sites}}", usageSites)
			content = strings.ReplaceAll(content, "{{requirement_background}}", a.args.Background)
			content = strings.ReplaceAll(content, "{{spec_cases}}", specCases)
			content = strings.ReplaceAll(content, "{{project_context}}", projectContext)
			// Curated see-also pointers; the reviewer fetches content on demand.
			content = strings.ReplaceAll(content, "{{see_also}}", seeAlso)
			// Run-level ranked symbol map (real names, anti-guessing).
			content = strings.ReplaceAll(content, "{{repo_map}}", a.repoMap)
			// A previous review's findings on this unit, to reconcile against the change.
			content = strings.ReplaceAll(content, "{{prior_findings}}", priorFindings)
			// Always substitute the {{plan_guidance}} token so the literal placeholder
			// never leaks into the rendered prompt. When the plan phase produced no
			// output, strip the surrounding "### Review Plan (Optional)\n…\n\n" wrapper
			// (any language variant) so the LLM does not see a dangling section header.
			// Strip MUST run before ReplaceAll: the regex requires the literal
			// {{plan_guidance}} token to be present; if we replace first, the token
			// is gone and the wrapper can't be matched.
			if planResult == "" {
				content = stripEmptyPlanBlock(content)
			}
			content = strings.ReplaceAll(content, "{{plan_guidance}}", planResult)
			messages = append(messages, llm.NewTextMessage(m.Role, content))
		}
		return messages
	}

	// The debrief is this unit's terminal record: what only this moment knows
	// (formation, source-preload fate, outcome) — post-hoc analysis can't rebuild it.
	// Cost rollup is filled by WriteDebrief from the scope's task records.
	deb := session.Debrief{
		Formed:       string(u.Formed),
		Fragments:    len(u.Fragments),
		Insertions:   u.Insertions(),
		Deletions:    u.Deletions(),
		Clues:        countClues(u.Clues),
		ClueRefs:     clueRefs(u.Clues),
		ContextPaths: cluePaths(u.Clues),
		UsageSites:   usageCount,
	}

	maxAllowed := a.args.Template.MaxTokens
	tokenLimit := maxAllowed * 4 / 5 // 80% of MaxTokens

	// Preloaded source is an optimization, never the reason a Unit is skipped:
	// related files drop before the reviewed source when the prompt is too large.
	ownFiles, relatedFiles, notes, outcomes := a.preloadReviewFiles(ctx, u)
	deb.SourcePreloads = outcomes
	domain := a.assembleReviewMessages(
		buildMessages, ownFiles, relatedFiles, notes, tokenLimit, &deb,
	)

	tokenCount := llm.CountMessagesTokens(msg.Lower(domain))
	if tokenCount > tokenLimit {
		msg := fmt.Sprintf("prompt tokens (%d) exceed %d%% of max_tokens(%d)", tokenCount, 80, maxAllowed)
		fmt.Fprintf(console.Out(), "[ccr] WARNING: %s for %s\n", msg, newPath)
		a.recordWarning("token_threshold_exceeded", newPath, msg)
		telemetry.Event(ctx, "token.threshold.exceeded",
			telemetry.AnyToAttr("file.path", newPath),
			telemetry.AnyToAttr("tokens", tokenCount),
			telemetry.AnyToAttr("max_tokens", maxAllowed))
		// A governor's decision, not a failure — the debrief must keep the two
		// distinguishable (lowering the threshold shifts skips, not the fault rate).
		deb.Outcome, deb.Reason = "skipped_policy", msg
		a.session.CloseScope(sc, deb)
		return nil
	}

	outcome, err := a.executor.Run(ctx, domain, sc)
	deb.Outcome, deb.Reason = outcome.State, outcome.Reason
	deb.BoardPulled = outcome.BoardPulled
	deb.BoardInjectedTokens = outcome.BoardInjectedTokens
	deb.BoardPosted = outcome.BoardPosted
	// Close ends the unit's lifecycle: the debrief persists now, or — when
	// async comment work is still in flight — the moment its last task ends.
	a.session.CloseScope(sc, deb)
	return err
}

// clueRefs collects the deduped symbol-ids a Unit's clues point at, in clue
// order — the debrief keeps content, not just counts (coverage counts can stay
// flat while every pointed-at symbol changes).
func clueRefs(clues []unit.Clue) []string {
	var refs []string
	for _, c := range clues {
		if c.Ref != "" {
			refs = append(refs, c.Ref)
		}
	}
	return slicesx.Uniq(refs)
}

// cluePaths preserves the relation that made a source file statically known
// to the Unit. The viewer compares these paths with later file_read calls;
// this is deliberately separate from SourcePreloads, which records what was
// actually placed in the initial prompt.
func cluePaths(clues []unit.Clue) map[string][]string {
	paths := map[string][]string{}
	for _, clue := range clues {
		var filePath string
		if clue.Relation == unit.RelProject {
			filePath = clue.Ref
		} else if path, _, ok := language.SplitSymbolID(clue.Ref); ok {
			filePath = path
		}
		if filePath == "" {
			continue
		}
		key := string(clue.Relation)
		paths[key] = append(paths[key], filePath)
	}
	for relation, values := range paths {
		paths[relation] = slicesx.Uniq(values)
	}
	return paths
}

// fileReader unwraps the file_read tool's FileReader so the preload reads exactly
// what the tool would return (same path resolution, same review-mode ref).
func (a *Runner) fileReader() *tool.FileReader {
	if p, ok := a.args.Tools.Get(tool.FileRead.Name()); ok {
		if frp, ok := p.(*tool.FileReadProvider); ok {
			return frp.FileReader
		}
	}
	return nil
}

// buildChangeFilesExcept returns a formatted list of changed files except the given path.
// buildChangeFilesExcept lists the other changed files for {{change_files}},
// excluding the Unit's own member file(s) — one path for a function/file Unit,
// several for a cross-file call-chain Unit (so a chain's own files aren't echoed
// back as "other" changes).
func (a *Runner) buildChangeFilesExcept(excludePaths ...string) string {
	exclude := make(map[string]bool, len(excludePaths))
	for _, p := range excludePaths {
		exclude[p] = true
	}
	var sb strings.Builder
	for i, d := range a.changes {
		if d.IsBinary {
			continue
		}
		if exclude[d.NewPath] || exclude[d.OldPath] {
			continue
		}
		status := "MODIFIED"
		switch {
		case d.IsNew:
			status = "ADDED"
		case d.IsDeleted:
			status = "DELETED"
		case d.OldPath != d.NewPath:
			status = "RENAMED"
		}
		sb.WriteString(status + "   " + d.NewPath)
		if i < len(a.changes)-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// resolveSystemRule returns the rule text for a given file path,
// matching against PathRuleMap glob patterns, falling back to DefaultRule.
func (a *Runner) resolveSystemRule(path string) string {
	if a.args.SystemRule == nil {
		return ""
	}
	return a.args.SystemRule.Resolve(path)
}

// filterLargeDiffs drops diffs whose diff content alone consumes more than 80% of MaxTokens.
func (a *Runner) filterLargeDiffs(changes []change.Change) []change.Change {
	limit := a.args.Template.MaxTokens * 4 / 5
	if limit <= 0 {
		return changes
	}
	var kept []change.Change
	skipped := 0

	for _, d := range changes {
		tokens := llm.CountTokens(d.Diff)
		if tokens > limit {
			fmt.Fprintf(console.Out(), "[ccr] Skipping %s (~%d tokens exceeds 80%% of max_tokens(%d))\n",
				d.NewPath, tokens, a.args.Template.MaxTokens)
			skipped++
			continue
		}
		kept = append(kept, d)
	}

	if skipped > 0 {
		fmt.Fprintf(console.Out(), "[ccr] Pre-filtered %d file(s) exceeding 80%% of max_tokens\n", skipped)
	}
	return kept
}

// countReviewable counts diffs that will survive all filters and are not pure deletions.
func (a *Runner) countReviewable(changes []change.Change) int {
	count := 0
	for _, d := range changes {
		if !a.shouldReview(d) {
			continue
		}
		if d.IsDeleted {
			continue
		}
		count++
	}
	return count
}

// shouldReview applies the filter algorithm via whyExcluded.
func (a *Runner) shouldReview(d change.Change) bool {
	if selection, ok := a.selectionFor(d); ok {
		return selection.Target
	}
	return a.whyExcluded(d) == ExcludeNone
}

// filterDiffs drops diffs that should not be reviewed based on user-configured
// include/exclude patterns and default extension/path filters.
func (a *Runner) filterDiffs(changes []change.Change) []change.Change {
	var kept []change.Change
	skipped := 0

	for _, d := range changes {
		path := effectivePath(d)
		if !a.shouldReview(d) {
			if selection, ok := a.selectionFor(d); ok && selection.Context {
				fmt.Fprintf(console.Out(), "[ccr] Using %s as %s component context\n", path, selection.Roles)
				continue
			} else if d.IsBinary {
				fmt.Fprintf(console.Out(), "[ccr] Skipping %s — binary file\n", path)
			} else {
				fmt.Fprintf(console.Out(), "[ccr] Skipping %s — filtered by path/extension rules\n", path)
			}
			skipped++
			continue
		}
		kept = append(kept, d)
	}

	if skipped > 0 {
		fmt.Fprintf(console.Out(), "[ccr] Filtered %d file(s) by include/exclude rules\n", skipped)
	}
	return kept
}

// extFromPath returns the file extension with leading dot, lowercased.
func (a *Runner) extFromPath(path string) string {
	basename := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		basename = path[idx+1:]
	}
	dot := strings.LastIndex(basename, ".")
	if dot <= 0 {
		return ""
	}
	return strings.ToLower(basename[dot:])
}

// executePlanPhase runs the plan task for a single file, sending template messages
// with resolved placeholders and collecting the LLM response as plan guidance.
func (a *Runner) executePlanPhase(ctx context.Context, sc session.Scope, rawDiff, changeFiles, rule string) (string, error) {
	newPath := sc.Path()
	pt := a.args.Template.PlanTask
	messages := make([]llm.Message, 0, len(pt.Messages))
	for _, m := range pt.Messages {
		content := m.Content
		content = strings.ReplaceAll(content, "{{current_system_date_time}}", a.currentDate)
		content = strings.ReplaceAll(content, "{{current_file_path}}", newPath)
		content = strings.ReplaceAll(content, "{{system_rule}}", rule)
		content = strings.ReplaceAll(content, "{{change_files}}", changeFiles)
		content = strings.ReplaceAll(content, "{{diff}}", rawDiff)
		content = strings.ReplaceAll(content, "{{requirement_background}}", a.args.Background)
		content = strings.ReplaceAll(content, "{{plan_tools}}", formatToolDefs(a.args.PlanToolDefs))
		messages = append(messages, llm.NewTextMessage(m.Role, content))
	}

	fs := a.session.GetOrCreateScope(sc)
	rec := fs.AppendTaskRecord(session.PlanTask, messages)
	startTime := time.Now()

	resp, err := a.args.LLMClient.CompletionsWithCtx(ctx, llm.ChatRequest{
		Model:     a.args.Model,
		Messages:  messages,
		MaxTokens: a.args.Template.MaxTokens,
	})
	if err != nil {
		rec.SetError(err, time.Since(startTime))
		return "", fmt.Errorf("plan request: %w", err)
	}
	rec.SetResponse(resp, time.Since(startTime))
	a.executor.RecordUsage(resp.Usage)
	fmt.Fprintf(console.Out(), "[ccr] Plan completed for %s\n", newPath)
	return resp.Content(), nil
}

// formatToolDefs renders tool definitions as human-readable text for embedding in prompts.
func formatToolDefs(toolDefs []llm.ToolDef) string {
	if len(toolDefs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("### Available Tools (reference only — do not call)\n")
	for _, td := range toolDefs {
		fn := &td.Function
		sb.WriteString(fmt.Sprintf("- **%s**: %s\n", fn.Name, fn.Description))
		if params, ok := fn.Parameters["properties"].(map[string]any); ok && len(params) > 0 {
			sb.WriteString("  Parameters:\n")
			required := make(map[string]bool)
			if reqList, ok := fn.Parameters["required"].([]any); ok {
				for _, r := range reqList {
					if s, ok := r.(string); ok {
						required[s] = true
					}
				}
			}
			for name, p := range params {
				suffix := ""
				if required[name] {
					suffix = " (required)"
				}
				if pm, ok := p.(map[string]any); ok {
					desc, _ := pm["description"].(string)
					sb.WriteString(fmt.Sprintf("  - %s: %s%s\n", name, desc, suffix))
				} else {
					sb.WriteString(fmt.Sprintf("  - %s%s\n", name, suffix))
				}
			}
		}
	}
	return sb.String()
}

// findChange returns the Diff for the given file path, or nil if not found.
func (a *Runner) findChange(path string) *change.Change {
	for i := range a.changes {
		if a.changes[i].NewPath == path || a.changes[i].OldPath == path {
			return &a.changes[i]
		}
	}
	return nil
}

// BuildToolDefs converts toolsconfig.ToolConfigEntry slice into []llm.ToolDef,
// filtering by phase (planOnly=true for plan_task, false for main_task).
func BuildToolDefs(entries []toolsconfig.ToolConfigEntry, planOnly bool) []llm.ToolDef {
	var defs []llm.ToolDef
	for _, e := range entries {
		defRaw, ok := e.ToolDefsByPhase(planOnly)
		if !ok {
			continue
		}
		var fn llm.FunctionDef
		if err := json.Unmarshal(defRaw, &fn); err != nil {
			fmt.Fprintf(console.Out(), "[ccr] WARNING: failed to parse tool definition %q: %v\n", e.Name, err)
			continue
		}
		defs = append(defs, llm.ToolDef{
			Type:     "function",
			Function: fn,
		})
	}
	return defs
}
