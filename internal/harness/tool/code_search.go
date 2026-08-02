package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	gitGrepMaxCount         = 100
	codeSearchMaxBatch      = 8
	codeSearchMaxBatchCount = 200
	gitGrepTimeout          = 10 * time.Second
)

// CodeSearchRequest is one independent query in code_search's batch-only
// contract. The stable shape lets the model confirm several known leads in a
// single turn instead of spending one LLM round per query.
type CodeSearchRequest struct {
	SearchText    string
	FilePatterns  []string
	CaseSensitive bool
	UsePerlRegexp bool
}

// ParseCodeSearchRequests parses searches[]. The former top-level
// search_text shape is intentionally not accepted by the runtime.
func ParseCodeSearchRequests(args map[string]any) ([]CodeSearchRequest, error) {
	values, ok := args["searches"].([]any)
	if !ok || len(values) == 0 {
		return nil, fmt.Errorf("searches must be a non-empty array")
	}
	if len(values) > codeSearchMaxBatch {
		return nil, fmt.Errorf("searches may contain at most %d items", codeSearchMaxBatch)
	}
	requests := make([]CodeSearchRequest, len(values))
	for i, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("searches[%d] must be an object", i)
		}
		requests[i] = CodeSearchRequest{
			SearchText:    stringValue(item["search_text"]),
			FilePatterns:  stringValues(item["file_patterns"]),
			CaseSensitive: boolValue(item["case_sensitive"]),
			UsePerlRegexp: boolValue(item["use_perl_regexp"]),
		}
	}
	return requests, nil
}

var codeSearchBatchHeader = regexp.MustCompile(`(?m)^===== CODE_SEARCH RESULT \d+/\d+ =====\n`)

// EncodeCodeSearchResults preserves request order and item-local failures in
// the single result required by the tool-call protocol.
func EncodeCodeSearchResults(results []string) string {
	var out strings.Builder
	for i, result := range results {
		if i > 0 {
			out.WriteByte('\n')
		}
		fmt.Fprintf(&out, "===== CODE_SEARCH RESULT %d/%d =====\n", i+1, len(results))
		out.WriteString(strings.TrimRight(result, "\n"))
		out.WriteByte('\n')
	}
	return out.String()
}

// DecodeCodeSearchResults splits the stable batch envelope.
func DecodeCodeSearchResults(result string) ([]string, bool) {
	matches := codeSearchBatchHeader.FindAllStringIndex(result, -1)
	if len(matches) == 0 || matches[0][0] != 0 {
		return nil, false
	}
	items := make([]string, len(matches))
	for i, match := range matches {
		end := len(result)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		items[i] = strings.TrimSpace(result[match[1]:end])
	}
	return items, true
}

// CodeSearchProvider performs text search across the repository using git grep.
type CodeSearchProvider struct {
	FileReader       *FileReader
	definitionSource CodeSearchDefinitionSource
}

func NewCodeSearch(fr *FileReader) *CodeSearchProvider { return &CodeSearchProvider{FileReader: fr} }

// WithDefinitionSource adds language-owned definitions to empty-search
// recovery without making the generic Harness depend on a source parser.
func (p *CodeSearchProvider) WithDefinitionSource(source CodeSearchDefinitionSource) *CodeSearchProvider {
	p.definitionSource = source
	return p
}

func (p *CodeSearchProvider) Tool() Tool { return CodeSearch }

func (p *CodeSearchProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	requests, err := ParseCodeSearchRequests(args)
	if err != nil {
		return "Error: " + err.Error(), nil
	}
	maxCount := min(gitGrepMaxCount, max(1, codeSearchMaxBatchCount/len(requests)))
	results := make([]string, len(requests))
	var wg sync.WaitGroup
	for i, request := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := p.executeOne(ctx, request, maxCount)
			if err != nil {
				result = "Error: " + err.Error()
			}
			results[i] = result
		}()
	}
	wg.Wait()
	return EncodeCodeSearchResults(results), nil
}

func (p *CodeSearchProvider) executeOne(
	ctx context.Context,
	request CodeSearchRequest,
	maxCount int,
) (string, error) {
	if strings.TrimSpace(request.SearchText) == "" {
		return "Error: search_text is blank", nil
	}
	for _, pattern := range request.FilePatterns {
		// A pathspec containing .. can make git grep read outside RepoDir.
		if hasTraversalPathComponent(pattern) {
			return "Error: file_patterns must not contain ..", nil
		}
	}
	result, err := p.gitGrepLimited(
		ctx,
		request.SearchText,
		request.CaseSensitive,
		request.UsePerlRegexp,
		request.FilePatterns,
		maxCount,
	)
	if err != nil {
		return "", fmt.Errorf("code_search failed: %w", err)
	}
	return result, nil
}

func (p *CodeSearchProvider) buildGrepArgs(searchText string, caseSensitive bool, usePerlRegexp bool, noIndex bool, pathspec []string) []string {
	return p.buildGrepArgsLimited(searchText, caseSensitive, usePerlRegexp, noIndex, pathspec, gitGrepMaxCount)
}

func (p *CodeSearchProvider) buildGrepArgsLimited(searchText string, caseSensitive bool, usePerlRegexp bool, noIndex bool, pathspec []string, maxCount int) []string {
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
	cmdArgs = append(cmdArgs, "--max-count", fmt.Sprintf("%d", maxCount))

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
	return p.gitGrepLimited(ctx, searchText, caseSensitive, usePerlRegexp, pathspec, gitGrepMaxCount)
}

func (p *CodeSearchProvider) gitGrepLimited(ctx context.Context, searchText string, caseSensitive bool, usePerlRegexp bool, pathspec []string, maxCount int) (string, error) {
	cmdArgs := p.buildGrepArgsLimited(searchText, caseSensitive, usePerlRegexp, false, pathspec, maxCount)

	outStr, errStr, err := p.runGitGrep(ctx, cmdArgs)

	// Non-git directory (`ccr scan` supports plain dirs): retry in --no-index
	// mode, which searches the working tree directly while honoring .gitignore.
	// We ask git whether this is a work tree rather than parsing its error text
	// (exit 128 + stderr substrings are locale-dependent and over-match). Skip on
	// ctx cancellation/timeout, and never retry ref-based search (needs a repo).
	if err != nil && p.FileReader.Ref == "" && ctx.Err() == nil && !p.insideGitWorkTree(ctx) {
		cmdArgs = p.buildGrepArgsLimited(searchText, caseSensitive, usePerlRegexp, true, pathspec, maxCount)
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
	truncated := len(lines) >= maxCount
	if len(lines) > maxCount {
		lines = lines[:maxCount]
	}

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
		sb.WriteString(fmt.Sprintf("Note: The results have been truncated. Only showing first %d results.\n", maxCount))
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

func stringValues(value any) []string {
	var out []string
	switch values := value.(type) {
	case []any:
		for _, item := range values {
			if text, ok := item.(string); ok && text != "" {
				out = append(out, text)
			}
		}
	case []string:
		for _, text := range values {
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
