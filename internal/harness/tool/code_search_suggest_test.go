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

func TestIdentifierParts(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"HandleCommand", []string{"command", "handle"}},
		{"run_command_now", []string{"command"}}, // run/now too short
		{"pkg.LoadConfig", []string{"config", "load"}},
		{"getX", nil},                  // all fragments < 4 chars
		{"ABCDef", []string{"abcdef"}}, // consecutive uppers stay one word
	}
	for _, tt := range tests {
		got := identifierParts(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("identifierParts(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("identifierParts(%q) = %v, want %v", tt.in, got, tt.want)
				break
			}
		}
	}
}

func TestDiceBigram(t *testing.T) {
	if s := diceBigram("handlecommand", "handlecommand"); s != 1 {
		t.Errorf("identical strings: got %v, want 1", s)
	}
	if s := diceBigram("handlecommand", "runcommand"); s < suggestMinScore {
		t.Errorf("related identifiers scored %v, want >= %v", s, suggestMinScore)
	}
	if s := diceBigram("handlecommand", "zzzz"); s != 0 {
		t.Errorf("unrelated strings scored %v, want 0", s)
	}
}

func setupSuggestRepo(t *testing.T) string {
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
	src := "package main\n\nfunc runCommand() {}\n\nfunc StartCommand() {}\n\nfunc runCommandLoop() { runCommand() }\n"
	if err := os.WriteFile(filepath.Join(dir, "cmd.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", ".")
	run("git", "commit", "-m", "init")
	return dir
}

func TestNoMatch_SuggestsSimilarIdentifiers(t *testing.T) {
	dir := setupSuggestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})
	// The agent guesses a name that doesn't exist; real identifiers sharing
	// the "command" fragment must come back as suggestions.
	result, err := p.gitGrep(context.Background(), "HandleCommand", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "No matches found") {
		t.Fatalf("expected no-match preamble, got: %s", result)
	}
	if !strings.Contains(result, "runCommand") && !strings.Contains(result, "StartCommand") {
		t.Errorf("expected similar identifiers suggested, got: %s", result)
	}
}

func TestNoMatch_PrefersLanguageDefinitions(t *testing.T) {
	dir := setupSuggestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace}).
		WithDefinitionSource(func(_ context.Context, paths []string) []CodeSearchDefinition {
			if !slices.Contains(paths, "cmd.go") {
				t.Fatalf("candidate paths = %v, want cmd.go", paths)
			}
			return []CodeSearchDefinition{
				{Name: "StartCommand", Path: "cmd.go", Line: 5},
				{Name: "runCommand", Path: "cmd.go", Line: 3},
			}
		})

	result, err := p.gitGrep(context.Background(), "HandleCommand", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Similar definitions from Language Knowledge") {
		t.Fatalf("expected language definition suggestions, got: %s", result)
	}
	if !strings.Contains(result, "StartCommand — cmd.go:5") {
		t.Errorf("expected definition location, got: %s", result)
	}
	if strings.Contains(result, "occurrence(s)") {
		t.Errorf("language definitions should replace text-only candidates, got: %s", result)
	}
}

func TestNoMatch_NoSuggestionsForRegexOrPhrases(t *testing.T) {
	dir := setupSuggestRepo(t)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: "", Mode: ModeWorkspace})

	// Perl-regex searches keep the plain message.
	result, err := p.gitGrep(context.Background(), "Handle.*Command", false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != "No matches found" {
		t.Errorf("regex search: expected plain message, got: %s", result)
	}

	// Phrases (spaces) are not identifier-like.
	result, err = p.gitGrep(context.Background(), "no such phrase here", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != "No matches found" {
		t.Errorf("phrase search: expected plain message, got: %s", result)
	}
}

func TestNoMatch_RefModeSuggestions(t *testing.T) {
	dir := setupSuggestRepo(t)
	commit := getHeadCommit(t, dir)
	p := NewCodeSearch(&FileReader{RepoDir: dir, Ref: commit, Mode: ModeCommit})
	result, err := p.gitGrep(context.Background(), "HandleCommand", false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "runCommand") && !strings.Contains(result, "StartCommand") {
		t.Errorf("ref-mode: expected suggestions, got: %s", result)
	}
}
