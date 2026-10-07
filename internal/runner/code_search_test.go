package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/gitcmd"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/runner/feature"
)

func TestCodeSearchDefinitionsReadsReviewedRef(t *testing.T) {
	repo := t.TempDir()
	runCodeSearchGit(t, repo, "init", "-q")
	runCodeSearchGit(t, repo, "config", "user.email", "test@example.com")
	runCodeSearchGit(t, repo, "config", "user.name", "Test User")
	runCodeSearchGit(t, repo, "config", "commit.gpgsign", "false")

	path := filepath.Join(repo, "sample.go")
	if err := os.WriteFile(path, []byte("package sample\n\nfunc OldName() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodeSearchGit(t, repo, "add", "sample.go")
	runCodeSearchGit(t, repo, "commit", "-q", "-m", "initial")
	ref := runCodeSearchGit(t, repo, "rev-parse", "HEAD")

	// The working tree has moved on, but a range/commit review must derive
	// suggestions from the reviewed ref rather than leak current source facts.
	if err := os.WriteFile(path, []byte("package sample\n\nfunc NewName() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reader := &tool.FileReader{
		RepoDir: repo,
		Mode:    tool.ModeCommit,
		Ref:     ref,
		Runner:  gitcmd.New(0),
	}
	source := NewCodeSearchLanguageSource(reader, language.NewAnalyzer(repo))
	provider := tool.NewCodeSearch(reader).WithDefinitionSource(source.Definitions)
	result, err := provider.Execute(context.Background(), map[string]any{
		"searches": []any{map[string]any{"query": "HandleName", "syntax": "literal"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "OldName — sample.go:3") || strings.Contains(result, "NewName") {
		t.Fatalf("result = %q, want OldName from reviewed ref only", result)
	}
}

func TestCodeSearchSymbolsReadReviewedRef(t *testing.T) {
	repo := t.TempDir()
	runCodeSearchGit(t, repo, "init", "-q")
	runCodeSearchGit(t, repo, "config", "user.email", "test@example.com")
	runCodeSearchGit(t, repo, "config", "user.name", "Test User")
	runCodeSearchGit(t, repo, "config", "commit.gpgsign", "false")

	path := filepath.Join(repo, "sample.go")
	oldSource := "package sample\n\nfunc OldName() {\n\tprintln(\"old\")\n}\n"
	if err := os.WriteFile(path, []byte(oldSource), 0o644); err != nil {
		t.Fatal(err)
	}
	runCodeSearchGit(t, repo, "add", "sample.go")
	runCodeSearchGit(t, repo, "commit", "-q", "-m", "initial")
	ref := runCodeSearchGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("package sample\n\nfunc NewName() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reader := &tool.FileReader{RepoDir: repo, Mode: tool.ModeCommit, Ref: ref, Runner: gitcmd.New(0)}
	source := NewCodeSearchLanguageSource(reader, language.NewAnalyzer(repo))
	provider := tool.NewCodeSearch(reader).WithSymbolSource(source.Symbols)
	result, err := provider.Execute(context.Background(), map[string]any{
		"searches": []any{map[string]any{"query": "old"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome, ok := tool.ParseCodeSearchSymbolContextOutcome(result)
	if !ok || outcome.Status != tool.CodeSearchSymbolExpanded ||
		!strings.Contains(result, "OldName") || strings.Contains(result, "NewName") {
		t.Fatalf("result = %q, outcome=%+v parsed=%t", result, outcome, ok)
	}
}

func runCodeSearchGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Search must identify the actual matched definition, including multiple hits
// within it, without substituting a similarly named telemetry helper (#119).
func TestCodeSearchSharedGraphEvidence(t *testing.T) {
	repo := t.TempDir()
	metric := "package telemetry\n\nfunc RecordToolCall(ok bool) {\n if !ok { println(\"RecordToolCall failed\") }\n}\n"
	for path, content := range map[string]string{
		"metrics.go": metric,
		"span.go":    "package telemetry\nfunc RecordToolResult(err error) {}\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reader := &tool.FileReader{RepoDir: repo, Mode: tool.ModeWorkspace}
	registry := tool.NewRegistry()
	provider := tool.NewCodeSearch(reader)
	registry.Register(provider)
	a := New(Args{RepoDir: repo, Tools: registry, Features: feature.Set{feature.SearchSymbolContext: true}})
	defer a.session.Finalize()
	index := a.analyzer.Repository()
	// Compare the visible tool result with local-only projection on identical input.
	local := NewCodeSearchLanguageSource(reader, language.NewAnalyzer(repo))
	baseline := tool.NewCodeSearch(reader).WithSymbolSource(local.Symbols)
	args := map[string]any{"searches": []any{map[string]any{"query": "RecordToolCall"}}}
	want, err := baseline.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Execute(context.Background(), args)
	if err != nil || got != want {
		t.Fatalf("projection drift: %v\nwant=%s\ngot=%s", err, want, got)
	}
	outcome, ok := tool.ParseCodeSearchSymbolContextOutcome(got)
	if !ok || outcome.Status != tool.CodeSearchSymbolExpanded || !strings.Contains(got, "if !ok") || strings.Contains(got, "RecordToolResult") {
		t.Fatalf("wrong evidence: %s", got)
	}
	if a.analyzer.Repository() != index {
		t.Fatal("tool replaced the shared graph")
	}
	// The existing gate still controls source expansion; no new model-facing mode.
	a.features = feature.Set{feature.SearchSymbolContext: false}
	a.configureSourceTools()
	got, err = provider.Execute(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	outcome, ok = tool.ParseCodeSearchSymbolContextOutcome(got)
	if ok && outcome.Status == tool.CodeSearchSymbolExpanded {
		t.Fatal("disabled source expansion still active")
	}
}
