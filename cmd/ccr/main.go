// case-code-review is an AI-powered code review CLI tool.
// It reads git diffs, sends them to a configurable LLM service, and generates review comments.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/telemetry"
)

func main() {
	llm.AppVersion = Version
	llm.InitEmbeddedLoader()

	ctx := context.Background()
	if telemetry.Init(ctx) {
		defer telemetry.ShutdownWithTimeout(ctx, 5*time.Second)
	}

	if err := dispatch(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// dispatch routes top-level subcommands or global flags.
func dispatch() error {
	args := os.Args[1:]

	// No args → default to review with empty args (will trigger usage/help)
	if len(args) == 0 {
		printTopLevelUsage()
		return nil
	}

	switch args[0] {
	case "--version", "-V":
		printVersion()
		return nil
	case "version":
		printVersion()
		return nil
	case "review", "r":
		return runReview(args[1:])
	case "scan", "s":
		return runScan(args[1:])
	case "config":
		return runConfig(args[1:])
	case "llm":
		return runLLM(args[1:])
	case "rules":
		return runRules(args[1:])
	case "viewer":
		return runViewer(args[1:])
	case "stats":
		return runStats(args[1:])
	case "export":
		return runExport(args[1:])
	case "-h", "--help":
		printTopLevelUsage()
		return nil
	default:
		return fmt.Errorf("unknown command: %s\nRun 'ocr' for usage", args[0])
	}
}

func printTopLevelUsage() {
	fmt.Println(`case-code-review - AI-Powered Code Review CLI

Usage:
  ccr [command]

Commands:
  review, r    Start a diff-based code review
  scan, s      Scan entire files (no diff required)
  rules        Inspect and debug review rules
  config       Manage configuration settings
  llm          LLM utility commands
  viewer       Start the WebUI session viewer
  stats        Analyze session transcripts (latency, tools, slow chains)
  export       Export session transcripts as ATIF trajectories
  version      Show version information

Examples:
  ccr review --from master --to dev        Review diff range
  ccr review --commit abc123               Review a single commit
  ccr scan                                 Scan every reviewable file in the repo
  ccr scan --path internal/runner           Scan a single directory
  ccr config provider                      Interactive provider setup
  ccr config model                         Interactive model selection
  ccr config set llm.model opus-4-6        Set a config value
  ccr llm test                             Test LLM connectivity
  ccr llm providers                        List built-in providers
  ccr version                              Show version info

Use "ccr review -h" for more information about review.
Use "ccr scan -h" for more information about scan.
Use "ccr rules -h" for more information about rules.
Use "ccr config" for more information about config.
Use "ccr llm" for more information about LLM utilities.

GitHub: https://github.com/qiankunli/case-code-review`)
}
