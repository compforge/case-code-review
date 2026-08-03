package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseCodeSearchRequestsDefaultsToLiteral(t *testing.T) {
	requests, err := ParseCodeSearchRequests(map[string]any{
		"searches": []any{map[string]any{"query": "Hello"}},
	})
	if err != nil || len(requests) != 1 || requests[0].Syntax != CodeSearchLiteral {
		t.Fatalf("default syntax = %#v, err=%v", requests, err)
	}

	requests, err = ParseCodeSearchRequests(map[string]any{
		"searches": []any{
			map[string]any{"query": "Hello", "syntax": "literal"},
			map[string]any{"query": "Hello.*", "syntax": "regexp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests[0].Syntax != CodeSearchLiteral || requests[1].Syntax != CodeSearchRegexp {
		t.Fatalf("parsed syntax = %#v", requests)
	}
	if _, err := ParseCodeSearchRequests(map[string]any{
		"searches": []any{map[string]any{"query": "Hello", "syntax": "glob"}},
	}); err == nil || !strings.Contains(err.Error(), "syntax must be literal or regexp") {
		t.Fatalf("invalid syntax error = %v", err)
	}
}

func TestCodeSearchResultPathsSupportsBatch(t *testing.T) {
	result := EncodeCodeSearchResults([]string{
		"File: a.go\nMatch lines: 1\n1|A\n\nFile: b.go\nMatch lines: 1\n2|B\n",
		"File: a.go\nMatch lines: 1\n3|A\n",
	})
	if got := CodeSearchResultPaths(result); !slices.Equal(got, []string{"a.go", "b.go"}) {
		t.Fatalf("paths = %v", got)
	}
}

func TestBuildGrepArgs_WorkspaceMode(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("myFunc", false, false, false, nil)

	assertContainsInOrder(t, args, "-e", "myFunc", "--")
	assertContains(t, args, "-i")
	if idx := slices.Index(args, "--"); idx >= 0 {
		for i := 0; i < idx; i++ {
			if args[i] == "myFunc" && (i == 0 || args[i-1] != "-e") {
				t.Error("myFunc should only appear as argument to -e, not as positional")
			}
		}
	}
}

func TestBuildGrepArgs_WorkspaceModeSearchesUntracked(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("myFunc", false, false, false, nil)
	// Workspace search must include freshly-added (untracked) files, else a new
	// file's content is invisible to the reviewer.
	assertContains(t, args, "--untracked")
}

func TestBuildGrepArgs_CommitModeNoUntracked(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: "abc1234"})
	args := p.buildGrepArgs("myFunc", false, false, false, nil)
	// Ref search reads a committed tree, where "untracked" is meaningless.
	if slices.Contains(args, "--untracked") {
		t.Error("--untracked must not be used in ref/commit mode")
	}
}

func TestBuildGrepArgs_NoIndexNoUntracked(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("myFunc", false, false, true, nil) // noIndex
	if slices.Contains(args, "--untracked") {
		t.Error("--untracked must not combine with --no-index")
	}
	assertContains(t, args, "--no-index")
}

func TestBuildGrepArgs_CommitMode(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: "abc1234"})
	args := p.buildGrepArgs("myFunc", false, false, false, []string{"pkg/"})

	assertContainsInOrder(t, args, "-e", "myFunc", "--end-of-options", "abc1234", "--", "pkg/")
}

func TestBuildGrepArgs_RefUsesEndOfOptions(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: "-O./pwn.sh"})
	args := p.buildGrepArgs("myFunc", false, false, false, nil)

	assertContainsInOrder(t, args, "-e", "myFunc", "--end-of-options", "-O./pwn.sh", "--")
}

func TestBuildGrepArgs_PatternStartingWithDash(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("-myOption", false, false, false, nil)

	idx := slices.Index(args, "-e")
	if idx < 0 || idx+1 >= len(args) || args[idx+1] != "-myOption" {
		t.Errorf("expected -e to immediately precede -myOption, got %v", args)
	}
}

func TestBuildGrepArgs_CaseSensitive(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("foo", true, false, false, nil)

	assertNotContains(t, args, "-i")
}

func TestBuildGrepArgs_CaseInsensitive(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("foo", false, false, false, nil)

	assertContains(t, args, "-i")
}

func TestBuildGrepArgs_PerlRegexp(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("foo", false, true, false, nil)

	assertContains(t, args, "-P")
	assertNotContains(t, args, "-F")
}

func TestBuildGrepArgs_FixedString(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: "/tmp", Ref: ""})
	args := p.buildGrepArgs("foo", false, false, false, nil)

	assertContains(t, args, "-F")
	assertNotContains(t, args, "-E")
	assertNotContains(t, args, "-P")
}

func setupTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("setup %v: %v\n%s", args, err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc Hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "util.go"), []byte("package pkg\n\nfunc Util() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "init")
	return dir
}

func getHeadCommit(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestInsideGitWorkTree(t *testing.T) {
	repo := setupTestRepo(t) // git-init'd
	plain := t.TempDir()     // no git

	if p := NewCodeSearch(&FileReader{RepoDir: repo}); !p.insideGitWorkTree(context.Background()) {
		t.Error("git-init'd dir should be detected as a work tree")
	}
	if p := NewCodeSearch(&FileReader{RepoDir: plain}); p.insideGitWorkTree(context.Background()) {
		t.Error("plain dir must not be detected as a work tree")
	}
}

func TestGitGrep_WorkspaceMode_Found(t *testing.T) {
	dir := setupTestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})
	result, err := p.gitGrep(context.Background(), "Hello", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "hello.go") {
		t.Errorf("expected hello.go in result, got: %s", result)
	}
}

func TestGitGrep_WorkspaceMode_NoMatch(t *testing.T) {
	dir := setupTestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})
	result, err := p.gitGrep(context.Background(), "nonexistentXYZ", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchOutcome(t, result, CodeSearchNoMatches, CodeSearchLiteral, 2)
}

func TestGitGrep_CommitMode_Found(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: commit, Mode: ModeCommit})
	result, err := p.gitGrep(context.Background(), "Hello", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "hello.go") {
		t.Errorf("expected hello.go in result, got: %s", result)
	}
	if !strings.Contains(result, "Match lines: 1") {
		t.Errorf("expected 1 match line, got: %s", result)
	}
}

func TestGitGrep_CommitMode_NoMatch(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: commit, Mode: ModeCommit})
	result, err := p.gitGrep(context.Background(), "nonexistentXYZ", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertSearchOutcome(t, result, CodeSearchNoMatches, CodeSearchLiteral, 2)
}

func TestGitGrep_CommitMode_WithPathspec(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: commit, Mode: ModeCommit})

	result, err := p.gitGrep(context.Background(), "Util", false, false, []string{"pkg/"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "util.go") {
		t.Errorf("expected util.go in result, got: %s", result)
	}

	result2, err2 := p.gitGrep(context.Background(), "Hello", false, false, []string{"pkg/"})
	if err2 != nil {
		t.Fatal(err2)
	}
	assertSearchOutcome(t, result2, CodeSearchNoMatches, CodeSearchLiteral, 1)
}

func TestGitGrep_CommitMode_GlobScopeUsesGrepPathspecSemantics(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: commit, Mode: ModeCommit})

	result, err := p.gitGrep(context.Background(), "nonexistentXYZ", false, false, []string{"**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	outcome, ok := ParseCodeSearchOutcome(result)
	if !ok || outcome.Status != CodeSearchNoMatches || outcome.SearchedFiles == nil || *outcome.SearchedFiles == 0 {
		t.Fatalf("glob scope outcome = %+v, parsed=%t; result=%q", outcome, ok, result)
	}
}

func TestCodeSearchExecuteHonorsRegexpSyntax(t *testing.T) {
	dir := setupTestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Mode: ModeWorkspace})
	out, err := p.Execute(context.Background(), map[string]any{
		"searches": []any{
			map[string]any{"query": "Hello.*", "syntax": "literal"},
			map[string]any{"query": "Hello.*", "syntax": "regexp"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, ok := DecodeCodeSearchResults(out)
	if !ok || len(results) != 2 {
		t.Fatalf("batch result = %q, parsed=%d ok=%t", out, len(results), ok)
	}
	literal, ok := ParseCodeSearchOutcome(results[0])
	if !ok || literal.Status != CodeSearchNoMatches || literal.QueryMode != CodeSearchLiteral {
		t.Fatalf("literal result = %+v, parsed=%t; result=%q", literal, ok, results[0])
	}
	if !strings.Contains(results[1], "hello.go") {
		t.Fatalf("regexp query did not match: %q", results[1])
	}
}

func TestGitGrep_OptionLikeRefDoesNotLaunchPager(t *testing.T) {
	dir := setupTestRepo(t)
	proofPath := filepath.Join(dir, "PROOF")
	pagerPath := filepath.Join(dir, "pwn.sh")
	if err := os.WriteFile(pagerPath, []byte("#!/bin/sh\nprintf pwned > PROOF\n"), 0755); err != nil {
		t.Fatal(err)
	}

	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "-O./pwn.sh", Mode: ModeCommit})
	result, err := p.gitGrep(context.Background(), "Hello", false, false, []string{"hello.go"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result, "Error:") {
		t.Fatalf("expected git error for invalid ref, got: %s", result)
	}
	if _, err := os.Stat(proofPath); err == nil {
		t.Fatal("option-like ref launched pager and created proof file")
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestGitGrep_CommitMode_WithBadPathspec(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: commit, Mode: ModeCommit})

	result, err := p.gitGrep(context.Background(), "Hello", false, false, []string{"nonexistent/"})
	if err != nil {
		t.Fatal(err)
	}
	assertSearchOutcome(t, result, CodeSearchScopeEmpty, CodeSearchLiteral, 0)
}

func TestGitGrep_LiteralWithRegexMetaChars(t *testing.T) {
	dir := setupTestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})
	result, err := p.gitGrep(context.Background(), "Hello()", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "hello.go") {
		t.Errorf("expected hello.go in result for literal 'Hello()' search, got: %s", result)
	}
}

func TestGitGrep_CommitMode_LiteralWithRegexMetaChars(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: commit, Mode: ModeCommit})
	result, err := p.gitGrep(context.Background(), "Hello()", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "hello.go") {
		t.Errorf("expected hello.go in result for literal 'Hello()' search at commit, got: %s", result)
	}
}

func TestGitGrep_InvalidRef_ReturnsError(t *testing.T) {
	dir := setupTestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "nonexistent_ref_abc123", Mode: ModeCommit})
	result, err := p.gitGrep(context.Background(), "Hello", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error message for invalid ref, got: %s", result)
	}
}

func TestGitGrep_PerlRegexp_InvalidPattern_ReturnsError(t *testing.T) {
	dir := setupTestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})
	result, err := p.gitGrep(context.Background(), "(unclosed", false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Error:") {
		t.Errorf("expected error message for invalid perl regexp, got: %s", result)
	}
}

func assertContains(t *testing.T, args []string, val string) {
	t.Helper()
	if !slices.Contains(args, val) {
		t.Errorf("expected args to contain %q, got %v", val, args)
	}
}

func assertNotContains(t *testing.T, args []string, val string) {
	t.Helper()
	if slices.Contains(args, val) {
		t.Errorf("expected args NOT to contain %q, got %v", val, args)
	}
}

func assertContainsInOrder(t *testing.T, args []string, vals ...string) {
	t.Helper()
	idx := 0
	for _, a := range args {
		if idx < len(vals) && a == vals[idx] {
			idx++
		}
	}
	if idx != len(vals) {
		t.Errorf("expected args to contain %v in order, got %v (matched up to index %d)", vals, args, idx)
	}
}

// TestGitGrep_NonGitDirectoryFallback verifies search_code works in a plain
// (non-git) directory by retrying git grep in --no-index mode instead of
// failing with git's exit 128, while still honoring .gitignore.
func TestGitGrep_NonGitDirectoryFallback(t *testing.T) {
	dir := t.TempDir() // plain dir, no `git init`

	write := func(rel, content string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("server.go", "package main\n\nfunc Handler() {}\n")
	write("internal/svc.go", "package internal\n\nfunc Handler() {}\n")
	write(".gitignore", "node_modules/\n")
	write("node_modules/lib.js", "function Handler() {}\n") // excluded by .gitignore

	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})

	out, err := p.gitGrep(context.Background(), "Handler", false, false, nil)
	if err != nil {
		t.Fatalf("gitGrep should not error in a non-git dir, got: %v", err)
	}
	if !strings.Contains(out, "server.go") || !strings.Contains(out, "internal/svc.go") {
		t.Errorf("expected matches in tracked-like files, got:\n%s", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Errorf("node_modules should be excluded via --exclude-standard, got:\n%s", out)
	}
}

// TestGitGrep_NonGitDirectoryNoMatch verifies the no-match path in a non-git
// dir returns the sentinel rather than an error.
func TestGitGrep_NonGitDirectoryNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})

	out, err := p.gitGrep(context.Background(), "nonexistentXYZ", false, false, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	outcome, ok := ParseCodeSearchOutcome(out)
	if !ok || outcome.Status != CodeSearchScopeUnknown || outcome.SearchedFiles != nil {
		t.Fatalf("plain-directory outcome = %+v, parsed=%t; result=%q", outcome, ok, out)
	}
}

func assertSearchOutcome(t *testing.T, result, status, queryMode string, searchedFiles int) {
	t.Helper()
	outcome, ok := ParseCodeSearchOutcome(result)
	if !ok || outcome.Status != status || outcome.QueryMode != queryMode ||
		outcome.SearchedFiles == nil || *outcome.SearchedFiles != searchedFiles {
		t.Fatalf("search outcome = %+v, parsed=%t; want status=%s mode=%s files=%d; result=%q",
			outcome, ok, status, queryMode, searchedFiles, result)
	}
}

func TestCodeSearch_RejectsTraversalPathspecs(t *testing.T) {
	p := &CodeSearchProvider{FileReader: &FileReader{RepoDir: t.TempDir()}}
	out, err := p.Execute(context.Background(), map[string]any{
		"searches": []any{map[string]any{
			"query":         "secret",
			"syntax":        "literal",
			"file_patterns": []any{"../outside/*.go"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "must not contain ..") {
		t.Fatalf("traversal pathspec must be rejected, got: %q", out)
	}
}

func TestCodeSearchBatchPreservesOrderAndItemErrors(t *testing.T) {
	dir := setupTestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Mode: ModeWorkspace})
	out, err := p.Execute(context.Background(), map[string]any{
		"searches": []any{
			map[string]any{"query": "Util", "syntax": "literal"},
			map[string]any{"query": "", "syntax": "literal"},
			map[string]any{"query": "Hello", "syntax": "literal"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, ok := DecodeCodeSearchResults(out)
	if !ok || len(results) != 3 {
		t.Fatalf("batch result = %q, parsed=%d ok=%t", out, len(results), ok)
	}
	if !strings.Contains(results[0], "pkg/util.go") ||
		!strings.HasPrefix(results[1], "Error: query is blank") ||
		!strings.Contains(results[2], "hello.go") {
		t.Fatalf("batch order/error isolation off: %#v", results)
	}
}

func TestCodeSearchBatchSharesAggregateMatchBudget(t *testing.T) {
	dir := setupTestRepo(t)
	content := strings.Repeat("alpha beta gamma\n", 100)
	if err := os.WriteFile(filepath.Join(dir, "many.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	p := NewCodeSearch(&FileReader{RepoDir: dir, Mode: ModeWorkspace})
	out, err := p.Execute(context.Background(), map[string]any{
		"searches": []any{
			map[string]any{"query": "alpha", "syntax": "literal"},
			map[string]any{"query": "beta", "syntax": "literal"},
			map[string]any{"query": "gamma", "syntax": "literal"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, ok := DecodeCodeSearchResults(out)
	if !ok || len(results) != 3 {
		t.Fatalf("batch result parsed=%d ok=%t", len(results), ok)
	}
	for i, result := range results {
		if got := strings.Count(result, "|alpha beta gamma"); got != 66 {
			t.Fatalf("result %d matches = %d, want 66", i, got)
		}
		if !strings.Contains(result, "first 66 results") {
			t.Fatalf("result %d missing shared-budget note: %q", i, result)
		}
	}
}

func TestCodeSearchRejectsFormerSingularShape(t *testing.T) {
	p := NewCodeSearch(&FileReader{RepoDir: t.TempDir(), Mode: ModeWorkspace})
	out, err := p.Execute(context.Background(), map[string]any{"query": "Hello"})
	if err != nil || !strings.Contains(out, "searches must be a non-empty array") {
		t.Fatalf("singular args = %q, %v; want batch-only contract error", out, err)
	}
}
