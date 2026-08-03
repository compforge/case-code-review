package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/qiankunli/case-code-review/internal/runner/feature"
)

// --- custom flag set that supports short flags (-c, -f etc.) ---

type ocrFlagSet struct {
	fs       *flag.FlagSet
	shortMap map[string]string // maps short key "c" -> full name "commit"
	showHelp bool
}

func newOcrFlagSet(name string) *ocrFlagSet {
	return &ocrFlagSet{
		fs:       flag.NewFlagSet(name, flag.ContinueOnError),
		shortMap: make(map[string]string),
	}
}

// StringVarP registers --name with optional short form -s.
func (a *ocrFlagSet) StringVarP(p *string, name, shorthand string, value, usage string) {
	suffix := ""
	if shorthand != "" {
		a.shortMap[shorthand] = name
		suffix = fmt.Sprintf(" (shorthand: -%s)", shorthand)
	}
	a.fs.StringVar(p, name, value, usage+suffix)
}

// BoolVarP registers --name with optional short form -s.
func (a *ocrFlagSet) BoolVarP(p *bool, name, shorthand string, value bool, usage string) {
	suffix := ""
	if shorthand != "" {
		a.shortMap[shorthand] = name
		suffix = fmt.Sprintf(" (shorthand: -%s)", shorthand)
	}
	a.fs.BoolVar(p, name, value, usage+suffix)
}

func (a *ocrFlagSet) StringVar(p *string, name string, value string, usage string) {
	a.fs.StringVar(p, name, value, usage)
}

func (a *ocrFlagSet) BoolVar(p *bool, name string, value bool, usage string) {
	a.fs.BoolVar(p, name, value, usage)
}

func (a *ocrFlagSet) IntVar(p *int, name string, value int, usage string) {
	a.fs.IntVar(p, name, value, usage)
}

func (a *ocrFlagSet) DurationVar(p *time.Duration, name string, value time.Duration, usage string) {
	a.fs.DurationVar(p, name, value, usage)
}

// repeatableString is a flag.Value that accumulates every occurrence of a flag,
// so `--feature a=off --feature b=off` yields ["a=off", "b=off"].
type repeatableString []string

func (r *repeatableString) String() string { return fmt.Sprintf("%v", []string(*r)) }
func (r *repeatableString) Set(v string) error {
	*r = append(*r, v)
	return nil
}

// StringSliceVar registers a repeatable --name flag.
func (a *ocrFlagSet) StringSliceVar(p *[]string, name, usage string) {
	a.fs.Var((*repeatableString)(p), name, usage)
}

func (a *ocrFlagSet) PrintDefaults() {
	a.fs.PrintDefaults()
}

func (a *ocrFlagSet) Parse(arguments []string) error {
	expanded := expandShortFlags(arguments, a.shortMap)

	for _, arg := range expanded {
		if arg == "-h" || arg == "--help" {
			a.showHelp = true
			return nil
		}
	}

	return a.fs.Parse(expanded)
}

// expandShortFlags replaces standalone -X args with their long equivalents.
// Only triggers when the arg is exactly -N (single char after dash).
func expandShortFlags(args []string, shortMap map[string]string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if len(arg) == 2 && arg[0] == '-' && arg[1] != '-' {
			key := string(arg[1])
			if full, ok := shortMap[key]; ok {
				out = append(out, "--"+full)
				continue
			}
		}
		out = append(out, arg)
	}
	return out
}

// --- review subcommand options ---

type reviewOptions struct {
	toolConfigPath string
	rulePath       string
	repoDir        string
	from           string
	to             string
	commit         string
	excludes       string // --exclude: comma-separated gitignore-style patterns
	outputFormat   string
	audience       string // --audience: "human" (default) or "agent"
	background     string // --background: optional requirement context
	bizID          string // --biz-id: caller-owned execution identity, persisted only
	specPath       string // --spec: path to spec.json (specgen output); also auto-loaded from .casecodereview/spec.json
	historyPath    string // --history: path to prior-findings JSON (symbol-id/path -> findings); injected per-unit so the reviewer reconciles them
	model          string // --model: override resolved LLM model for this review
	concurrency    int
	perFileTimeout int
	maxTools       int
	maxGitProcs    int
	preview        bool
	dryRun         bool
	features       []string // --feature name=on|off (repeatable): ablation gates; see internal/runner/feature
	showHelp       bool
}

func parseReviewFlags(args []string) (reviewOptions, error) {
	a := newOcrFlagSet("ccr review")

	opts := reviewOptions{}

	a.StringVar(&opts.toolConfigPath, "tools", "", "path to JSON tools config file (default: embedded)")
	a.StringVar(&opts.rulePath, "rule", "", "path to JSON file with system review rules")
	a.StringVar(&opts.repoDir, "repo", "", "root directory of the git repository (default: current dir)")
	a.StringVar(&opts.from, "from", "", "source ref to start diff from (e.g., 'main')")
	a.StringVar(&opts.to, "to", "", "target ref to end diff at (e.g., 'feature-branch')")
	a.StringVarP(&opts.commit, "commit", "c", "", "single commit hash or tag to review (vs its parent)")
	a.StringVar(&opts.excludes, "exclude", "", "comma-separated gitignore-style patterns to exclude; merged with rule.json excludes")
	a.StringVarP(&opts.outputFormat, "format", "f", "text", "output format: text, json, or jsonl")
	a.StringSliceVar(&opts.features, "feature", "toggle a feature gate: name=on|off (repeatable); run 'ccr review --help' for the list")
	a.IntVar(&opts.concurrency, "concurrency", 8, "max concurrent file reviews")
	a.IntVar(&opts.perFileTimeout, "timeout", 10, "concurrent task timeout in minutes")
	a.StringVar(&opts.audience, "audience", "human", "output audience: human (show progress) or agent (summary only)")
	a.StringVarP(&opts.background, "background", "b", "", "optional requirement/business context for the review")
	a.StringVar(&opts.bizID, "biz-id", "", "opaque caller-owned business identity persisted with the session")
	a.StringVar(&opts.specPath, "spec", "", "path to spec.json (specgen output); also auto-loaded from .casecodereview/spec.json — injects each changed function's spec/case as a contract checklist")
	a.StringVar(&opts.historyPath, "history", "", "path to a prior-findings JSON (symbol-id or repo-relative path -> findings); injected per review unit so the reviewer reconciles whether the change addresses them")
	a.StringVar(&opts.model, "model", "", "override LLM model for this review (e.g., claude-opus-4-6)")
	a.IntVar(&opts.maxTools, "max-tools", 0, "max tool call rounds per file (0 = template default; min 10)")
	a.IntVar(&opts.maxGitProcs, "max-git-procs", 16, "max concurrent git subprocesses")
	a.BoolVarP(&opts.preview, "preview", "p", false, "no-LLM, file level: list which files will be reviewed")
	a.BoolVar(&opts.dryRun, "dry-run", false, "no-LLM, full view: the --preview file list plus each review unit's assembled context (spec/case/rule/link + caller/callee)")

	if err := a.Parse(args); err != nil {
		return opts, fmt.Errorf("parse flags: %w", err)
	}

	opts.showHelp = a.showHelp
	if opts.showHelp {
		return opts, nil
	}

	modeCount := 0
	if opts.from != "" || opts.to != "" {
		modeCount++
	}
	if opts.commit != "" {
		modeCount++
	}
	// modeCount == 0 → workspace mode (no error, allowed)
	if modeCount > 1 {
		return opts, fmt.Errorf("only one review mode allowed (--from/--to or --commit)")
	}
	if opts.from != "" && opts.to == "" {
		return opts, fmt.Errorf("--to is required when --from is specified")
	}
	if opts.to != "" && opts.from == "" {
		return opts, fmt.Errorf("--from is required when --to is specified")
	}

	switch opts.audience {
	case "human", "agent":
	default:
		return opts, fmt.Errorf("invalid --audience value %q: must be 'human' or 'agent'", opts.audience)
	}
	switch opts.outputFormat {
	case "text", "json", "jsonl":
	default:
		return opts, fmt.Errorf("invalid --format value %q: must be 'text', 'json', or 'jsonl'", opts.outputFormat)
	}
	if opts.outputFormat == "jsonl" && (opts.preview || opts.dryRun) {
		return opts, fmt.Errorf("--format jsonl is only supported for an executing review")
	}

	const minMaxTools = 10
	if opts.maxTools < 0 {
		return opts, fmt.Errorf("--max-tools must be a non-negative integer (0 means use template default)")
	}
	if opts.maxTools > 0 && opts.maxTools < minMaxTools {
		fmt.Fprintf(os.Stderr, "[ccr] --max-tools %d is below minimum %d, using %d\n", opts.maxTools, minMaxTools, minMaxTools)
		opts.maxTools = minMaxTools
	}

	if opts.maxGitProcs < 0 {
		return opts, fmt.Errorf("--max-git-procs must be a non-negative integer (0 means use default 16)")
	}

	return opts, nil
}

func printReviewUsage() {
	fmt.Println(`case-code-review - AI-Powered Code Review CLI

Usage:
  ccr review [flags]
  ccr r [flags]                (alias)

Examples:
  # Review staged + unstaged + untracked changes in current workspace
  ccr review

  # Review a branch against its base (merge-base mode)
  ccr review --from master --to dev-ref

  # Review a specific commit
  ccr review --commit abc123
  ccr review -c abc123

  # Output JSON format
  ccr review --format json
  ccr review -f json

  # Stream accepted findings as JSON Lines while the review is running
  ccr review --format jsonl

  # Agent mode (summary only, no progress lines)
  ccr review --audience agent

  # Preview which files will be reviewed
  ccr review --preview
  ccr review -c abc123 -p

  # Full no-LLM view: the --preview file list + each unit's assembled context
  ccr review --dry-run
  ccr review --dry-run --format json   # + structural metrics (unit/scope/clue counts) for free A/B

  # Inject contract context from a spec.json (specgen output; also auto-loaded from .casecodereview/spec.json)
  ccr review --spec spec.json

Flags:
  --audience string       output audience: human (show progress) or agent (summary only) (default "human")
  -b, --background string optional requirement/business context for the review
  --biz-id string         opaque caller-owned business identity persisted with the session
  -c, --commit string     single commit hash or tag to review (vs its parent)
  -f, --format string     output format: text, json, or jsonl (default "text")
  --feature name=on|off   toggle a feature gate (repeatable); also config features:{} / CCR_FEATURES env
  --concurrency int       max concurrent file reviews (default 8)
  --max-git-procs int     max concurrent git subprocesses (default 16)
  --from string           source ref to start diff from (e.g., 'main')
  --max-tools int         max tool call rounds per file (0 = template default; min 10)
  --model string          override LLM model for this review (e.g., claude-opus-4-6)
  -p, --preview           no-LLM, file level: list which files will be reviewed
  --dry-run               no-LLM, full view: the --preview file list plus each review unit's assembled context (spec/case/rule/link + caller/callee)
  --repo string           root directory of the git repository (default: current dir)
  --rule string           path to JSON file with system review rules
  --spec string           path to spec.json (specgen output); also auto-loaded from .casecodereview/spec.json
  --history string        path to prior-findings JSON (symbol-id/path → findings); injected per unit so the reviewer reconciles them
  --timeout int           concurrent task timeout in minutes (default 10)
  --to string             target ref to end diff at (e.g., 'feature-branch')
  --tools string          path to JSON tools config file (default: embedded)`)

	fmt.Println("\nFeature gates (--feature name=on|off; leave-one-out ablation, all default on):")
	for _, line := range feature.Describe() {
		fmt.Println("  " + line)
	}
}

// --- config subcommand ---

type configAction struct {
	subCmd string // "set", "unset"
	key    string
	value  string
}

func parseConfigArgs(args []string) (configAction, error) {
	if len(args) == 0 {
		return configAction{}, fmt.Errorf("usage: ccr config set <key> <value>\ne.g., ccr config set llm.model claude-opus-4-6")
	}

	subCmd := args[0]
	switch subCmd {
	case "set":
		if len(args) < 3 {
			return configAction{}, fmt.Errorf("usage: ccr config set <key> <value>\ne.g., ccr config set llm.model claude-opus-4-6")
		}
		return configAction{
			subCmd: "set",
			key:    args[1],
			value:  args[2],
		}, nil
	case "unset":
		if len(args) < 2 {
			return configAction{}, fmt.Errorf("usage: ccr config unset custom_providers.<name>\ne.g., ccr config unset custom_providers.my-gateway")
		}
		return configAction{
			subCmd: "unset",
			key:    args[1],
		}, nil
	default:
		return configAction{}, fmt.Errorf("unknown config sub-command: %s\nAvailable: set, unset, provider, model", subCmd)
	}
}

func printConfigUsage() {
	fmt.Println(`Configuration management.

Usage:
  ccr config set <key> <value>
  ccr config unset custom_providers.<name>  Delete a custom provider
  ccr config provider                       Interactive provider setup
  ccr config model                          Interactive model selection

Examples:
  # Provider setup (interactive)
  ccr config provider
  ccr config model

  # Provider setup (non-interactive)
  ccr config set provider anthropic
  ccr config set model claude-opus-4-6
  # Set API key via environment variable (recommended) or config:
  # export ANTHROPIC_API_KEY=sk-ant-xxx
  ccr config set providers.anthropic.api_key "$ANTHROPIC_API_KEY"

  # Custom provider
  ccr config set provider my-gateway
  ccr config set custom_providers.my-gateway.url https://gateway.internal.com/v1
  ccr config set custom_providers.my-gateway.protocol openai
  ccr config set custom_providers.my-gateway.model llama-3-70b
  ccr config set custom_providers.my-gateway.models '["llama-3-70b","llama-3-8b"]'
  ccr config set custom_providers.my-gateway.api_key "$MY_API_KEY"

  # Delete a custom provider
  ccr config unset custom_providers.my-gateway

  # Legacy endpoint configuration
  ccr config set llm.url https://xx/v1/openai/chat/completions
  ccr config set llm.auth_token xxxxxxxxxx
  ccr config set llm.auth_header x-api-key
  ccr config set llm.model claude-opus-4-6
  ccr config set llm.extra_body '{"thinking":{"type":"disabled"}}'
  ccr config set language English
  ccr config set telemetry.enabled true

Supported keys: provider, model, providers.<name>.<field>, custom_providers.<name>.<field>, llm.url, llm.auth_token, llm.auth_header, llm.model, llm.use_anthropic, llm.extra_body, language, telemetry.enabled, telemetry.exporter, telemetry.otlp_endpoint, telemetry.content_logging
Provider fields: api_key, url, protocol, model, models, auth_header, extra_body`)
}
