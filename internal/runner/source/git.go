package source

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/compforge/repocli/toolkit/go"
	allowedext "github.com/qiankunli/case-code-review/internal/config/allowlist"
	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/sourceview"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// DiffContextLines defines the number of context lines around each changed hunk.
const DiffContextLines = 3

// Mode defines how the diff is retrieved.
type Mode int

const (
	ModeWorkspace Mode = iota // current workspace (staged + unstaged + untracked)
	ModeCommit                // single commit vs its parent
	ModeRange                 // merge-base(from,to)..to
)

// Provider retrieves and parse git diffs from a repository.
type Provider struct {
	repoDir string
	mode    Mode
	runner  *gitcmd.Runner

	// Range mode parameters
	from, to string // from/to refs for range comparison

	// Commit mode parameter
	commit string // single commit hash/ref

	Before, After *sourceview.Snapshot
	Diff          repocli.DiffReport

	base      string
	baseKnown bool
	mergeBase string // cached common ancestor for range mode
}

// NewProvider creates a Provider for range mode: from..to (via merge-base).
func NewProvider(repoDir, from, to string, runner *gitcmd.Runner) *Provider {
	return &Provider{
		repoDir: repoDir,
		mode:    ModeRange,
		from:    from,
		to:      to,
		runner:  runner,
	}
}

// NewCommitProvider creates a Provider for commit mode: show changes introduced by a single commit.
func NewCommitProvider(repoDir, commit string, runner *gitcmd.Runner) *Provider {
	return &Provider{
		repoDir: repoDir,
		mode:    ModeCommit,
		commit:  commit,
		runner:  runner,
	}
}

// NewWorkspaceProvider creates a Provider for workspace mode (current uncommitted changes).
func NewWorkspaceProvider(repoDir string, runner *gitcmd.Runner) *Provider {
	return &Provider{
		repoDir: repoDir,
		mode:    ModeWorkspace,
		runner:  runner,
	}
}

// IsRangeMode returns true when comparing two refs.
func (p *Provider) IsRangeMode() bool {
	return p.mode == ModeRange
}

// IsCommitMode returns true when analyzing a single commit.
func (p *Provider) IsCommitMode() bool {
	return p.mode == ModeCommit
}

// MergeBase returns the computed merge-base commit hash for range mode.
func (p *Provider) MergeBase(ctx context.Context) string {
	if p.mode != ModeRange || p.mergeBase != "" {
		return p.mergeBase
	}
	p.mergeBase = p.computeMergeBase(ctx, p.from, p.to)
	return p.mergeBase
}

// BaseRef returns the tree immediately before the reviewed change. Range mode
// uses the merge base (matching GetDiff), workspace uses HEAD, and commit mode
// uses the first parent. Empty means an empty-tree baseline (root commit).
func (p *Provider) BaseRef(ctx context.Context) (ref string) {
	if p.baseKnown {
		return p.base
	}
	defer func() { p.base = ref; p.baseKnown = true }()
	switch p.mode {
	case ModeRange:
		return p.MergeBase(ctx)
	case ModeWorkspace:
		sha, err := p.runGit(ctx, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return ""
		}
		return strings.TrimSpace(sha)
	case ModeCommit:
		out, err := p.runGit(
			ctx, "rev-parse", "--verify", "--end-of-options", p.commit+"^1^{commit}",
		)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(out)
	default:
		return ""
	}
}

// GetDiff returns all changes as parsed Diff structs.
func (p *Provider) GetDiff(ctx context.Context) ([]change.Change, error) {
	// Resolve symbolic targets once before diff/content acquisition. Every
	// subsequent query, including graph construction, uses these exact commits.
	if p.mode == ModeRange || p.mode == ModeCommit {
		target := p.to
		if p.mode == ModeCommit {
			target = p.commit
		}
		sha, err := p.runGit(ctx, "rev-parse", "--verify", "--end-of-options", target+"^{commit}")
		if err != nil {
			return nil, fmt.Errorf("resolve review target: %w", err)
		}
		if p.mode == ModeCommit {
			p.commit = strings.TrimSpace(sha)
		} else {
			p.to = strings.TrimSpace(sha)
		}
	}
	base := p.BaseRef(ctx)
	if p.mode == ModeRange && base == "" {
		return nil, fmt.Errorf("cannot find merge-base between %s and %s", p.from, p.to)
	}
	target := ""
	if p.mode == ModeRange {
		target = p.to
	} else if p.mode == ModeCommit {
		target = p.commit
	}
	captured, err := repocli.Diff(ctx, repocli.DiffRequest{TagRules: allowedext.TagRules(), Repository: p.repoDir, Base: base, EmptyBase: base == "", Head: target})
	if err != nil {
		return nil, err
	}
	p.Diff = captured
	p.Before = sourceview.NewCaptured(captured, true)
	p.After = sourceview.NewCaptured(captured, false)
	return captured.Changes, nil
}

// ---- Internal helpers ----

func (p *Provider) computeMergeBase(ctx context.Context, from, to string) string {
	out, err := p.runGit(ctx, "merge-base", "--end-of-options", from, to)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (p *Provider) runGit(ctx context.Context, args ...string) (string, error) {
	if p.runner != nil {
		return p.runner.Run(ctx, p.repoDir, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = p.repoDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
