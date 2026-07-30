package runner

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/qiankunli/case-code-review/internal/harness/session"
)

// planBlockPattern matches the optional "Review Plan" section in a MAIN_TASK
// template user message: a header line beginning with "### " whose text
// contains "Review Plan" or "审查计划" (with optional ASCII "(Optional)" /
// Chinese "（可选）" suffix), the {{plan_guidance}} placeholder on its own
// line, and one trailing blank line. The ASCII and Chinese header forms
// are matched separately because Go's regexp engine does not define \b
// around CJK ideographs.
var planBlockPattern = regexp.MustCompile(
	`(?m)^### [^\n]*(?:Review Plan|审查计划)[^\n]*\n\{\{plan_guidance\}\}\n\n?`)

// stripEmptyPlanBlock removes the "### Review Plan …\n{{plan_guidance}}\n\n"
// wrapper from a MAIN_TASK user message when the plan phase produced no
// guidance. The previous implementation hard-coded a single Chinese literal,
// which did not match the actual English template shipped in
// task_template.json, so the literal token "{{plan_guidance}}" leaked into
// the rendered prompt on every review where the plan phase was skipped or
// failed. Strip is a no-op when the wrapper is absent.
func stripEmptyPlanBlock(content string) string {
	return planBlockPattern.ReplaceAllString(content, "")
}

func reviewModeString(from, to, commit string) string {
	if commit != "" {
		return session.ReviewModeCommit
	}
	if from != "" && to != "" {
		return session.ReviewModeRange
	}
	return session.ReviewModeWorkspace
}

// detectGitBranch returns the current git branch name for the given repo, or empty string on failure.
func detectGitBranch(ctx context.Context, repoDir string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// detectGitHead returns the repo's HEAD sha — the manifest's anchor for
// posterior scans ("did a later commit touch what a finding flagged"), which
// need to know exactly which state the review looked at. In workspace mode
// it's the base the dirty tree sits on; diffFrom/diffTo already pin the
// range/commit modes.
func detectGitHead(ctx context.Context, repoDir string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil || len(out) == 0 {
		return ""
	}
	return strings.TrimSpace(string(out))
}
