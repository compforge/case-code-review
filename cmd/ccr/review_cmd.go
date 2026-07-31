package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/runner"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit/history"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

func runReview(args []string) error {
	opts, err := parseReviewFlags(args)
	if err != nil {
		// parseReviewFlags already wraps with "parse flags: %w" — return as-is.
		return err
	}
	if opts.showHelp {
		printReviewUsage()
		return nil
	}

	// review path: git repo is required (diff concepts depend on it).
	cc, err := loadCommonContext(opts.repoDir, opts.rulePath, opts.maxTools, opts.maxGitProcs, true)
	if err != nil {
		return err
	}
	applyCLIExcludes(cc, splitPaths(opts.excludes))

	// Security (#112): reject ref-option injection before any git invocation.
	if err := validateReviewRefs(cc.RepoDir, opts); err != nil {
		return err
	}

	if opts.commit != "" && opts.background == "" {
		if msg, err := getCommitMessage(cc.RepoDir, opts.commit); err == nil && msg != "" {
			opts.background = msg
		}
	}

	if opts.preview {
		return runPreview(cc, opts)
	}
	if opts.dryRun {
		return runDryRun(cc, opts)
	}

	features, err := resolveFeatures(opts.features)
	if err != nil {
		return err
	}
	rt, err := loadLLMRuntime(cc.Template, opts.toolConfigPath, opts.model, features.Enabled(feature.Routing))
	if err != nil {
		return err
	}

	mode := tool.ParseReviewMode(opts.from, opts.to, opts.commit)
	ref, _ := mode.RefValue(opts.to, opts.commit)
	fileReader := &tool.FileReader{
		RepoDir: cc.RepoDir,
		Mode:    mode,
		Ref:     ref,
		Runner:  cc.GitRunner,
	}
	baseReader := &tool.FileReader{RepoDir: cc.RepoDir, Mode: tool.ModeCommit, Runner: cc.GitRunner}
	tools := buildToolRegistry(rt.Findings, fileReader, baseReader)

	// Loads the --spec path plus auto-discovered .casecodereview/spec.json layers, mirroring
	// how rules are resolved. Nil when no layer exists.
	specs, err := spec.Load(cc.RepoDir, opts.specPath)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	historyIndex, err := history.Load(opts.historyPath)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}

	ag := runner.New(runner.Args{
		RepoDir:               cc.RepoDir,
		From:                  opts.from,
		To:                    opts.to,
		Commit:                opts.commit,
		Template:              *cc.Template,
		SystemRule:            cc.Resolver,
		FileFilter:            cc.FileFilter,
		LLMClient:             rt.Client,
		Tools:                 tools,
		PlanToolDefs:          rt.PlanToolDefs,
		MainToolDefs:          rt.MainToolDefs,
		Findings:              rt.Findings,
		WorkerPool:            harness.NewWorkerPool(opts.concurrency),
		MaxConcurrency:        opts.concurrency,
		ConcurrentTaskTimeout: opts.perFileTimeout,
		Model:                 rt.Model,
		Background:            opts.background,
		Specs:                 specs,
		HistoryIndex:          historyIndex,
		GitRunner:             cc.GitRunner,
		Features:              features,
		Version:               versionString(),
	})

	// Silence progress output during execution; restored before the trace
	// summary in agent-text mode (and on function exit otherwise).
	q := newQuietHandle(opts.outputFormat, opts.audience)
	defer q.Restore()

	ctx, span := telemetry.StartSpan(context.Background(), "review.run")
	defer span.End()
	startTime := time.Now()

	comments, err := ag.Run(ctx)
	if err != nil {
		telemetry.SetAttr(span, "error", err.Error())
		// Fatal or not, json mode must put one JSON object on stdout — the
		// exit code still signals failure for CLI callers.
		if opts.outputFormat == "json" {
			_ = outputJSONFatal(err, ag.Warnings())
		}
		return fmt.Errorf("review failed: %w", err)
	}

	return emitRunResult(ctx, ag, comments, startTime, opts.outputFormat, opts.audience, q)
}

func resolveRepoDir(input string) (string, error) {
	if input == "" {
		var err error
		input, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("get working directory: %w", err)
		}
	}
	absPath, err := filepath.Abs(input)
	if err != nil {
		return "", fmt.Errorf("resolve absolute path: %w", err)
	}
	out, err := runGitCmd(absPath, "rev-parse", "--git-dir")
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("%s is not a git repository", absPath)
	}
	return absPath, nil
}

// requireGitRepo validates that the given directory is part of a git repository.
func requireGitRepo(dir string) error {
	repoDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	out, err := runGitCmd(repoDir, "rev-parse", "--git-dir")
	if err != nil || len(out) == 0 {
		return fmt.Errorf("%s is not a git repository, code review requires a valid git repository", repoDir)
	}
	return nil
}

// validateReviewRefs rejects ref-option injection (#112): any --from/--to/
// --commit value must be a real commit ref and must not start with '-'.
func validateReviewRefs(repoDir string, opts reviewOptions) error {
	refs := []struct {
		flag string
		ref  string
	}{
		{"--from", opts.from},
		{"--to", opts.to},
		{"--commit", opts.commit},
	}
	for _, item := range refs {
		if item.ref == "" {
			continue
		}
		if strings.HasPrefix(item.ref, "-") {
			return fmt.Errorf("%s value %q is not a valid git ref: refs must not start with '-'", item.flag, item.ref)
		}
		if out, err := runGitCmd(repoDir, "rev-parse", "--verify", "--end-of-options", item.ref+"^{commit}"); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg != "" {
				return fmt.Errorf("%s value %q is not a valid commit ref: %s", item.flag, item.ref, msg)
			}
			return fmt.Errorf("%s value %q is not a valid commit ref", item.flag, item.ref)
		}
	}
	return nil
}

func runPreview(cc *commonContext, opts reviewOptions) error {
	ag := runner.New(runner.Args{
		RepoDir:    cc.RepoDir,
		From:       opts.from,
		To:         opts.to,
		Commit:     opts.commit,
		FileFilter: cc.FileFilter,
		GitRunner:  cc.GitRunner,
	})

	preview, err := ag.Preview(context.Background())
	if err != nil {
		return fmt.Errorf("preview failed: %w", err)
	}

	outputPreviewText(preview)
	return nil
}

// runDryRun assembles and prints each review unit's context (spec/case/rule/link
// + caller/callee) without an LLM call — needs the spec index + rule resolver,
// but no LLM runtime, so it works whether or not the LLM is configured.
func runDryRun(cc *commonContext, opts reviewOptions) error {
	specs, err := spec.Load(cc.RepoDir, opts.specPath)
	if err != nil {
		return fmt.Errorf("load spec: %w", err)
	}
	historyIndex, err := history.Load(opts.historyPath)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}
	features, err := resolveFeatures(opts.features)
	if err != nil {
		return err
	}
	ag := runner.New(runner.Args{
		RepoDir:      cc.RepoDir,
		From:         opts.from,
		To:           opts.to,
		Commit:       opts.commit,
		FileFilter:   cc.FileFilter,
		GitRunner:    cc.GitRunner,
		Specs:        specs,
		HistoryIndex: historyIndex,
		SystemRule:   cc.Resolver,
		Background:   opts.background,
		Features:     features,
		Version:      versionString(),
	})

	preview, units, repoMap, err := ag.DryRun(context.Background())
	if err != nil {
		return fmt.Errorf("dry-run failed: %w", err)
	}
	if opts.outputFormat == "json" {
		return outputDryRunJSON(preview, units, repoMap, features.Resolved())
	}
	outputPreviewText(preview) // which files are reviewed/excluded (the --preview view)
	outputDryRunText(units)    // each unit's assembled context
	dryRunSection("Repo Symbol Map (run-level)", repoMap)
	return nil
}

func buildToolRegistry(
	findings *finding.Collector,
	fr *tool.FileReader,
	base *tool.FileReader,
) *tool.Registry {
	reg := tool.NewRegistry()
	reg.Register(tool.NewFileRead(fr))
	if base != nil {
		reg.Register(tool.NewFileReadBase(base))
	}
	reg.Register(tool.NewFileFind(fr))
	reg.Register(tool.NewFileReadDiff(tool.DiffMap{}))
	reg.Register(tool.NewCodeSearch(fr))
	reg.Register(&finding.ToolProvider{Collector: findings})
	return reg
}
