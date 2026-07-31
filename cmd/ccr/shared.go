package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/rules"
	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/config/toolsconfig"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// commonContext bundles the state that both `ocr review` and `ocr scan`
// need to load *before* deciding whether to dispatch a preview or a real
// LLM session: a validated template, the resolved repo path, review rules,
// and a shared git subprocess limiter.
type commonContext struct {
	Template   *template.Template
	RepoDir    string
	Resolver   rules.Resolver
	FileFilter *rules.FileFilter
	GitRunner  *gitcmd.Runner
	// IsGitRepo reports whether RepoDir is inside a git repository. Always
	// true when requireGit was set; may be false when scan accepts non-git
	// directories.
	IsGitRepo bool
}

// loadCommonContext validates the working directory, loads the embedded
// template, raises MaxToolRequestTimes when maxTools exceeds the default,
// resolves the absolute repo path, loads system review rules, and creates
// the global git subprocess limiter. Both review and scan callers go
// through this so the startup sequence stays consistent.
//
// requireGit=true fails fast when the directory is not a git repo (review
// path: diff concept requires git). requireGit=false allows non-git
// directories (scan path: provider falls back to filepath.Walk).
func loadCommonContext(repoDirInput, rulePath string, maxTools, maxGitProcs int, requireGit bool) (*commonContext, error) {
	tpl, err := template.LoadDefault()
	if err != nil {
		return nil, fmt.Errorf("load default template: %w", err)
	}
	if maxTools > tpl.MaxToolRequestTimes {
		tpl.MaxToolRequestTimes = maxTools
	}
	if err := tpl.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	repoDir, isGit, err := resolveWorkingDir(repoDirInput, requireGit)
	if err != nil {
		return nil, err
	}

	resolver, fileFilter, err := rules.NewResolver(repoDir, rulePath)
	if err != nil {
		return nil, fmt.Errorf("load rules: %w", err)
	}

	return &commonContext{
		Template:   tpl,
		RepoDir:    repoDir,
		Resolver:   resolver,
		FileFilter: fileFilter,
		GitRunner:  gitcmd.New(maxGitProcs),
		IsGitRepo:  isGit,
	}, nil
}

// resolveWorkingDir returns (absPath, isGitRepo, err). When requireGit is
// true, returns an error if the directory is not a git repo. When false,
// returns IsGitRepo=false instead of erroring (scan path uses this).
func resolveWorkingDir(input string, requireGit bool) (string, bool, error) {
	if input == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", false, fmt.Errorf("get working directory: %w", err)
		}
		input = wd
	}
	absPath, err := filepath.Abs(input)
	if err != nil {
		return "", false, fmt.Errorf("resolve absolute path: %w", err)
	}
	if _, statErr := os.Stat(absPath); statErr != nil {
		return "", false, fmt.Errorf("stat %s: %w", absPath, statErr)
	}
	out, err := runGitCmd(absPath, "rev-parse", "--git-dir")
	isGit := err == nil && len(out) > 0
	if !isGit && requireGit {
		return "", false, fmt.Errorf("%s is not a git repository", absPath)
	}
	return absPath, isGit, nil
}

// llmRuntime bundles the LLM-side state both subcommands need once they've
// decided to actually run a session: tool definitions, an app-language
// adjusted template (mutated in place via ApplyLanguage), the LLM client,
// the resolved model name, and a fresh comment collector.
type llmRuntime struct {
	Client       llm.LLMClient
	Model        string
	PlanToolDefs []llm.ToolDef
	MainToolDefs []llm.ToolDef
	Findings     *finding.Collector
	AppCfg       *Config
}

// resolveFeatures layers feature-gate config in precedence order: registry
// defaults < config file (features:{}) < CCR_FEATURES env < --feature CLI.
// Unknown gate names are a hard error (typo protection). Best-effort on the
// config file (missing/unreadable → defaults).
func resolveFeatures(cliFeatures []string) (feature.Set, error) {
	var configLayer map[feature.Gate]bool
	if cfgPath, err := defaultConfigPath(); err == nil {
		if appCfg, err := LoadAppConfig(cfgPath); err == nil && appCfg != nil && len(appCfg.Features) > 0 {
			configLayer = make(map[feature.Gate]bool, len(appCfg.Features))
			for k, v := range appCfg.Features {
				configLayer[feature.Gate(k)] = v
			}
		}
	}
	envLayer, err := feature.Parse(os.Getenv("CCR_FEATURES"))
	if err != nil {
		return nil, err
	}
	cliLayer, err := feature.Parse(strings.Join(cliFeatures, ","))
	if err != nil {
		return nil, err
	}
	return feature.Resolve(configLayer, envLayer, cliLayer)
}

// loadLLMRuntime loads tool defs from toolConfigPath, reads the app config
// from the user's default config path (applying the configured language to
// tpl — defaulting when the config file is absent), resolves the LLM
// endpoint (honoring modelOverride from --model when non-empty; routingEnabled
// off collapses a multi-model pool to a single deterministic model), and
// returns the runtime bundle. tpl is mutated in place.
func loadLLMRuntime(tpl *template.Template, toolConfigPath, modelOverride string, routingEnabled bool) (*llmRuntime, error) {
	toolEntries, err := toolsconfig.Load(toolConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load tools: %w", err)
	}
	planToolDefs := runner.BuildToolDefs(toolEntries, true)
	mainToolDefs := runner.BuildToolDefs(toolEntries, false)

	cfgPath, err := defaultConfigPath()
	if err != nil {
		return nil, err
	}
	appCfg, err := LoadAppConfig(cfgPath)
	if err != nil {
		return nil, fmt.Errorf("load app config: %w", err)
	}
	// Apply the language directive even when the config file is missing
	// (upstream #fix: ApplyLanguage with empty lang falls back to default).
	var lang string
	if appCfg != nil {
		lang = appCfg.Language
	}
	tpl.ApplyLanguage(lang)

	eps, policy, err := llm.ResolveModelsWithModelOverride(cfgPath, modelOverride)
	if err != nil {
		return nil, fmt.Errorf("resolve LLM endpoint: %w", err)
	}
	if !routingEnabled && len(eps) > 1 {
		// routing gate off: collapse to a single model (deterministic — no
		// round-robin variance). NewLLMRouter with a 1-pool returns a plain client.
		eps, policy = eps[:1], ""
	}

	return &llmRuntime{
		Client:       llm.NewLLMRouter(eps, policy),
		Model:        eps[0].Model,
		PlanToolDefs: planToolDefs,
		MainToolDefs: mainToolDefs,
		Findings:     finding.NewCollector(),
		AppCfg:       appCfg,
	}, nil
}

// applyCLIExcludes appends user-supplied --exclude patterns (already split
// into a []string) onto cc.FileFilter.Exclude. Creates the FileFilter if
// none was returned by rule.json layers. Idempotent on empty input.
func applyCLIExcludes(cc *commonContext, patterns []string) {
	if len(patterns) == 0 {
		return
	}
	if cc.FileFilter == nil {
		cc.FileFilter = &rules.FileFilter{}
	}
	cc.FileFilter.Exclude = append(cc.FileFilter.Exclude, patterns...)
}

// excludeToolDef returns a copy of defs with any entries whose function name
// matches name removed. Used by `ocr scan` to hide tools that don't make
// sense in full-scan mode (e.g. file_read_diff).
func excludeToolDef(defs []llm.ToolDef, name string) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(defs))
	for _, d := range defs {
		if d.Function.Name == name {
			continue
		}
		out = append(out, d)
	}
	return out
}

// quietHandle wraps a console.Quiet() restorer so callers can `defer
// q.Restore()` for safety while emitRunResult restores it early when the
// agent-text audience needs the trace summary on the user's terminal.
// Restore is idempotent.
type quietHandle struct {
	fn func()
}

// newQuietHandle silences stdout when outputFormat=="json" or
// audience=="agent"; otherwise the returned handle is a no-op restorer.
func newQuietHandle(outputFormat, audience string) *quietHandle {
	h := &quietHandle{}
	if outputFormat == "json" || audience == "agent" {
		h.fn = console.Quiet()
	}
	return h
}

// Restore re-enables stdout. Safe to call multiple times.
func (h *quietHandle) Restore() {
	if h == nil || h.fn == nil {
		return
	}
	h.fn()
	h.fn = nil
}

// ResultProvider abstracts the metadata both internal/runner.Runner and
// internal/runner/scan.Runner expose post-run, so emitRunResult can finalize
// either without knowing which kind it has.
type ResultProvider interface {
	Changes() []change.Change
	FilesReviewed() int64
	TotalInputTokens() int64
	TotalOutputTokens() int64
	TotalTokensUsed() int64
	TotalCacheReadTokens() int64
	TotalCacheWriteTokens() int64
	Warnings() []runner.Warning
	// ProjectSummary is the markdown project-level summary produced by
	// scan's PROJECT_SUMMARY_TASK. Empty for review mode and for scans
	// that skipped / failed the summary phase.
	ProjectSummary() string
	ToolCalls() map[string]int64
	// ModelsUsed maps each routing alias that served a response to its count
	// (deduped). The review's model identity; empty for a single-model run.
	ModelsUsed() map[string]int
}

// emitRunResult is the post-LLM-run finalization shared by `ocr review` and
// `ocr scan`: resolves comment line numbers, records telemetry, restores
// stdout early for agent-text audiences so the summary is visible, prints
// the trace summary, and writes the result in the requested format.
//
// q is the silencing handle returned by newQuietHandle; pass nil if no
// silencing was set up (in which case the early restore is a no-op).
func emitRunResult(
	ctx context.Context,
	ag ResultProvider,
	comments []finding.Finding,
	startTime time.Time,
	outputFormat, audience string,
	q *quietHandle,
) error {
	comments = finding.ResolveLineNumbers(comments, ag.Changes())

	duration := time.Since(startTime)
	telemetry.RecordReviewDuration(ctx, duration)
	if len(comments) > 0 {
		telemetry.RecordCommentsGenerated(ctx, int64(len(comments)))
	}

	if outputFormat == "json" && len(comments) == 0 && ag.FilesReviewed() == 0 {
		return outputJSONNoFiles()
	}

	// Agent-text audiences need stdout back before PrintTraceSummary so the
	// summary line lands on their terminal.
	if audience == "agent" && outputFormat != "json" {
		q.Restore()
	}

	if outputFormat != "json" {
		telemetry.PrintTraceSummary(ag.FilesReviewed(), int64(len(comments)),
			ag.TotalInputTokens(), ag.TotalOutputTokens(), ag.TotalTokensUsed(),
			ag.TotalCacheReadTokens(), ag.TotalCacheWriteTokens(), duration)
	}

	if outputFormat == "json" {
		return outputJSONWithWarnings(comments, ag.Warnings(), ag.FilesReviewed(),
			ag.TotalInputTokens(), ag.TotalOutputTokens(), ag.TotalTokensUsed(),
			ag.TotalCacheReadTokens(), ag.TotalCacheWriteTokens(), duration,
			ag.ProjectSummary(), ag.ToolCalls(), ag.ModelsUsed())
	}
	outputTextWithWarnings(comments, ag.Warnings())
	if summary := ag.ProjectSummary(); summary != "" {
		fmt.Printf("\n\n──────── Project Summary ────────\n\n%s\n", summary)
	}
	return nil
}
