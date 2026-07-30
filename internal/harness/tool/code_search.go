package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const (
	gitGrepMaxCount = 100
	gitGrepTimeout  = 10 * time.Second
)

// CodeSearchProvider performs text search across the repository using git grep.
type CodeSearchProvider struct {
	FileReader *FileReader
}

func NewCodeSearch(fr *FileReader) *CodeSearchProvider { return &CodeSearchProvider{FileReader: fr} }

func (p *CodeSearchProvider) Tool() Tool { return CodeSearch }

func (p *CodeSearchProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	searchText, _ := args["search_text"].(string)
	caseSensitive, _ := args["case_sensitive"].(bool)
	usePerlRegexp, _ := args["use_perl_regexp"].(bool)

	filePatternsIface, _ := args["file_patterns"].([]any)
	var patterns []string
	for _, item := range filePatternsIface {
		if s, ok := item.(string); ok && s != "" {
			// 信任边界（docs/reviewer-trust-boundary）：pathspec 带 .. 能让 git grep
			// 读到仓外内容，reviewer 的只读承诺仅限 repo 内——直接拒绝。
			if hasTraversalPathComponent(s) {
				return "Error: file_patterns must not contain ..", nil
			}
			patterns = append(patterns, s)
		}
	}

	if strings.TrimSpace(searchText) == "" {
		return "Error: search_text is blank", nil
	}

	result, err := p.gitGrep(ctx, searchText, caseSensitive, usePerlRegexp, patterns)
	if err != nil {
		return "", fmt.Errorf("code_search failed: %w", err)
	}
	return result, nil
}

func (p *CodeSearchProvider) buildGrepArgs(searchText string, caseSensitive bool, usePerlRegexp bool, noIndex bool, pathspec []string) []string {
	cmdArgs := []string{"--no-pager", "grep"}

	if noIndex {
		// Non-git directory: search the working tree directly while still
		// honoring .gitignore and skipping .git (via --exclude-standard).
		cmdArgs = append(cmdArgs, "--no-index", "--exclude-standard")
	} else if p.FileReader.Ref == "" {
		// Workspace search: git grep defaults to tracked files only, missing
		// freshly-added (not-yet-`git add`ed) files. --untracked covers both.
		// Ref-based search reads a committed tree, where untracked has no meaning.
		cmdArgs = append(cmdArgs, "--untracked")
	}

	if !caseSensitive {
		cmdArgs = append(cmdArgs, "-i")
	}
	if usePerlRegexp {
		cmdArgs = append(cmdArgs, "-P")
	} else {
		cmdArgs = append(cmdArgs, "-F")
	}

	cmdArgs = append(cmdArgs, "-n", "--no-color")
	cmdArgs = append(cmdArgs, "--max-count", fmt.Sprintf("%d", gitGrepMaxCount))

	cmdArgs = append(cmdArgs, "-e", searchText)

	if ref := p.FileReader.Ref; ref != "" {
		cmdArgs = append(cmdArgs, "--end-of-options")
		cmdArgs = append(cmdArgs, ref)
	}

	cmdArgs = append(cmdArgs, "--")
	cmdArgs = append(cmdArgs, pathspec...)

	return cmdArgs
}

func (p *CodeSearchProvider) runGitGrep(parentCtx context.Context, cmdArgs []string) (string, string, error) {
	ctx, cancel := context.WithTimeout(parentCtx, gitGrepTimeout)
	defer cancel()

	if p.FileReader.Runner != nil {
		stdout, stderr, err := p.FileReader.Runner.RunSplit(ctx, p.FileReader.RepoDir, cmdArgs...)
		if ctx.Err() != nil && err != nil {
			return "", "", ctx.Err()
		}
		return stdout, stderr, err
	}

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = p.FileReader.RepoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil && err != nil && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == -1 {
		return "", "", ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}

func (p *CodeSearchProvider) gitGrep(ctx context.Context, searchText string, caseSensitive bool, usePerlRegexp bool, pathspec []string) (string, error) {
	cmdArgs := p.buildGrepArgs(searchText, caseSensitive, usePerlRegexp, false, pathspec)

	outStr, errStr, err := p.runGitGrep(ctx, cmdArgs)

	// Non-git directory (`ccr scan` supports plain dirs): retry in --no-index
	// mode, which searches the working tree directly while honoring .gitignore.
	// We ask git whether this is a work tree rather than parsing its error text
	// (exit 128 + stderr substrings are locale-dependent and over-match). Skip on
	// ctx cancellation/timeout, and never retry ref-based search (needs a repo).
	if err != nil && p.FileReader.Ref == "" && ctx.Err() == nil && !p.insideGitWorkTree(ctx) {
		cmdArgs = p.buildGrepArgs(searchText, caseSensitive, usePerlRegexp, true, pathspec)
		outStr, errStr, err = p.runGitGrep(ctx, cmdArgs)
	}

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "code_search timed out. Try narrowing file_patterns to a more specific path.", nil
		}
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		if outStr == "" {
			if errStr == "" {
				return p.noMatchesWithSuggestions(ctx, searchText, usePerlRegexp, pathspec), nil
			}
			return fmt.Sprintf("Error: %s", strings.TrimSpace(errStr)), nil
		}
	}

	lines := strings.Split(strings.TrimRight(outStr, "\n"), "\n")
	truncated := len(lines) >= gitGrepMaxCount

	type match struct {
		lineNum int
		content string
	}
	fileMatches := make(map[string][]match)
	var fileOrder []string
	seen := make(map[string]bool)

	hasRef := p.FileReader.Ref != ""
	splitN := 3
	offset := 0
	if hasRef {
		splitN = 4
		offset = 1
	}

	var sb strings.Builder
	if truncated {
		sb.WriteString(fmt.Sprintf("Note: The results have been truncated. Only showing first %d results.\n", gitGrepMaxCount))
	}

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", splitN)
		if len(parts) < splitN {
			continue
		}
		fname := parts[offset]
		m := match{}
		ln, parseErr := strconv.Atoi(parts[offset+1])
		if parseErr != nil {
			continue
		}
		m.lineNum = ln
		m.content = parts[offset+2]
		if !seen[fname] {
			seen[fname] = true
			fileOrder = append(fileOrder, fname)
		}
		fileMatches[fname] = append(fileMatches[fname], m)
	}

	for _, path := range fileOrder {
		matches := fileMatches[path]
		sb.WriteString(fmt.Sprintf("File: %s\nMatch lines: %d\n", path, len(matches)))
		for _, m := range matches {
			sb.WriteString(fmt.Sprintf("%d|%s\n", m.lineNum, m.content))
		}
		sb.WriteString("\n")
	}

	if err != nil && errStr != "" {
		sb.WriteString(fmt.Sprintf("Warning: %s\n", strings.TrimSpace(errStr)))
	}

	return sb.String(), nil
}

// insideGitWorkTree reports whether RepoDir sits inside a git work tree. Lets git
// itself decide repo-ness (locale-independent, exact) so the --no-index fallback
// is chosen precisely — instead of guessing from exit codes / stderr substrings.
// Any failure (not a repo, git missing, ctx done) yields false → fall back.
func (p *CodeSearchProvider) insideGitWorkTree(ctx context.Context) bool {
	args := []string{"rev-parse", "--is-inside-work-tree"}
	var out string
	var err error
	if p.FileReader.Runner != nil {
		out, _, err = p.FileReader.Runner.RunSplit(ctx, p.FileReader.RepoDir, args...)
	} else {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = p.FileReader.RepoDir
		var b []byte
		b, err = cmd.Output()
		out = string(b)
	}
	return err == nil && strings.TrimSpace(out) == "true"
}

// hasTraversalPathComponent reports whether a pathspec contains a `..` path
// component. git grep resolves pathspecs relative to the repo dir, so `..`
// escapes the repository — the one thing the read-only reviewer must never do.
func hasTraversalPathComponent(pathspec string) bool {
	for _, part := range strings.Split(pathspec, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}
