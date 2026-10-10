package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/runner"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit/history"
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
	if opts.maxCompletionTokens > 0 {
		cc.Template.MaxCompletionTokens = opts.maxCompletionTokens
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
	rt, err := loadLLMRuntime(
		cc.Template, opts.toolConfigPath, opts.model,
		llmRuntimeOptions{
			routingEnabled: features.Enabled(feature.Routing),
		},
	)
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
	tools := buildToolRegistry(
		rt.Findings, fileReader, baseReader, features.Enabled(feature.SearchSymbolContext),
	)

	historyIndex, err := history.Load(opts.historyPath)
	if err != nil {
		return fmt.Errorf("load history: %w", err)
	}
	var stream *jsonlEmitter
	if opts.outputFormat == "jsonl" {
		stream = newJSONLEmitter(os.Stdout)
	}

	stopCleanup := startSessionCleanup(rt.AppCfg)
	defer stopCleanup()

	ag := runner.New(runner.Args{
		MaxSnapshotBytes:      opts.maxSnapshotBytes,
		MaxFiles:              opts.maxFiles,
		RepoDir:               cc.RepoDir,
		From:                  opts.from,
		To:                    opts.to,
		Commit:                opts.commit,
		Template:              *cc.Template,
		SystemRule:            cc.Resolver,
		FileFilter:            cc.FileFilter,
		LLMClient:             rt.Client,
		MaxTokensBudget:       int64(opts.maxTokensBudget),
		Tools:                 tools,
		PlanToolDefs:          rt.PlanToolDefs,
		MainToolDefs:          rt.MainToolDefs,
		Findings:              rt.Findings,
		WorkerPool:            harness.NewWorkerPool(opts.concurrency),
		MaxConcurrency:        opts.concurrency,
		MaxUnits:              opts.maxUnits,
		ConcurrentTaskTimeout: opts.perFileTimeout,
		Model:                 rt.Model,
		Background:            opts.background,
		BizID:                 opts.bizID,
		SpecPath:              opts.specPath,
		HistoryIndex:          historyIndex,
		GitRunner:             cc.GitRunner,
		Features:              features,
		Version:               versionString(),
		OnFinding: func(comment finding.Finding) {
			if stream != nil {
				stream.finding(comment)
			}
		},
	})

	// Silence progress output during execution; restored before the trace
	// summary in agent-text mode (and on function exit otherwise).
	q := newQuietHandle(opts.outputFormat, opts.audience)
	defer q.Restore()
	if stream != nil {
		stream.start(ag.Session())
	}

	ctx, span := telemetry.StartSpan(context.Background(), "review.run")
	defer span.End()
	startTime := time.Now()

	comments, err := ag.Run(ctx)
	if err != nil {
		telemetry.SetAttr(span, "error", err.Error())
		// Fatal or not, json mode must put one JSON object on stdout — the
		// exit code still signals failure for CLI callers.
		if opts.outputFormat == "json" {
			_ = outputJSONFatal(err, ag.Warnings(), ag.Session())
		} else if stream != nil {
			stream.finish(buildJSONFatal(err, ag.Warnings(), ag.Session()))
		}
		return fmt.Errorf("review failed: %w", err)
	}
	if stream != nil {
		return emitJSONLRunResult(ctx, ag, comments, startTime, stream)
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
		MaxSnapshotBytes: opts.maxSnapshotBytes,
		MaxFiles:         opts.maxFiles,
		RepoDir:          cc.RepoDir,
		From:             opts.from,
		To:               opts.to,
		Commit:           opts.commit,
		FileFilter:       cc.FileFilter,
		GitRunner:        cc.GitRunner,
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
	if opts.outputFormat == "json" {
		defer console.Quiet()()
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
		MaxSnapshotBytes: opts.maxSnapshotBytes,
		MaxFiles:         opts.maxFiles,
		RepoDir:          cc.RepoDir,
		Template:         *cc.Template,
		From:             opts.from,
		To:               opts.to,
		Commit:           opts.commit,
		FileFilter:       cc.FileFilter,
		GitRunner:        cc.GitRunner,
		SpecPath:         opts.specPath,
		HistoryIndex:     historyIndex,
		SystemRule:       cc.Resolver,
		Background:       opts.background,
		Features:         features,
		Version:          versionString(),
		MaxUnits:         opts.maxUnits,
	})

	preview, units, repoMap, err := ag.DryRun(context.Background())
	if err != nil {
		return fmt.Errorf("dry-run failed: %w", err)
	}
	if opts.outputFormat == "json" {
		return outputDryRunJSON(preview, units, repoMap, features.Resolved(), ag.CodeGraphReports(), ag.GroupingReport())
	}
	outputPreviewText(preview) // which files are reviewed/excluded (the --preview view)
	outputDryRunText(units)    // each unit's assembled context
	if report := ag.GroupingReport(); report.LimitExceeded {
		fmt.Printf("Grouping target not reached: %d Units remain (target %d); source relationships or size budgets prevent further grouping.\n", report.FinalUnits, report.MaxUnits)
	}
	dryRunSection("Repo Symbol Map (run-level)", repoMap)
	return nil
}

func buildToolRegistry(
	findings *finding.Collector,
	fr *tool.FileReader,
	base *tool.FileReader,
	searchSymbolContextEnabled bool,
) *tool.Registry {
	reg := tool.NewRegistry()
	reg.Register(tool.NewFileRead(fr))
	dependencyReader := language.NewGoDependencyReader(fr.Read, tool.FileReadMaxLines, tool.MaxResultBytes)
	reg.Register(tool.NewBuiltin(tool.ReadGoDependency, dependencyReader.Execute))
	if base != nil {
		reg.Register(tool.NewFileReadBase(base))
	}
	reg.Register(tool.NewFileFind(fr))
	reg.Register(tool.NewFileReadDiff(tool.DiffMap{}))
	codeSearchLanguage := runner.NewCodeSearchLanguageSource(fr)
	codeSearch := tool.NewCodeSearch(fr).WithDefinitionSource(codeSearchLanguage.Definitions)
	if searchSymbolContextEnabled {
		codeSearch.WithSymbolSource(codeSearchLanguage.Symbols)
	}
	reg.Register(codeSearch)
	reg.Register(&finding.ToolProvider{Collector: findings})
	return reg
}
