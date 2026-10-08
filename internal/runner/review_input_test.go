package runner

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestCapturedPR139CluesReduceActualProjection(t *testing.T) {
	data, err := os.ReadFile("testdata/pr139-clues.json")
	if err != nil {
		t.Fatal(err)
	}
	var clues []unit.Clue
	if err := json.Unmarshal(data, &clues); err != nil {
		t.Fatal(err)
	}
	if len(clues) != 22 {
		t.Fatal(len(clues))
	}
	_, documents := separateClueDocuments(clues)
	view := harness.ProjectContext(documents, msg.Artifacts(documents), true)
	var content strings.Builder
	for _, message := range view {
		content.WriteString(message.TextContent())
		content.WriteByte('\n')
	}
	original, _, _, _ := renderClues(clues)
	oldTokens, newTokens := llm.CountTokens(original), llm.CountTokens(content.String())
	if newTokens >= oldTokens {
		t.Fatalf("real doc distribution expanded: before=%d after=%d", oldTokens, newTokens)
	}
	for _, clue := range clues {
		if !strings.Contains(content.String(), clue.Ref) || (clue.Snapshot != "" && !strings.Contains(content.String(), clue.Snapshot)) {
			t.Fatal("lost provenance", clue.Ref)
		}
	}
	t.Logf("PR139 22 docs: before=%d after=%d estimated tokens", oldTokens, newTokens)
}

func TestDryRunUsesExecutionInputAndProjection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := t.TempDir()
	runSelectionGit(t, repo, "init", "-q")
	runSelectionGit(t, repo, "config", "user.name", "Test")
	runSelectionGit(t, repo, "config", "user.email", "test@example.com")
	writeSelectionContent(t, repo, "go.mod", "module example.com/review\n\ngo 1.26\n")
	writeSelectionContent(t, repo, "a.go", "package p\n// Value returns the configured value.\nfunc Value() int { return 1 }\n")
	runSelectionGit(t, repo, "add", ".")
	runSelectionGit(t, repo, "commit", "-qm", "base")
	writeSelectionContent(t, repo, "a.go", "package p\n// Value returns the configured value.\nfunc Value() int { return 2 }\n")
	tmpl, err := template.LoadDefault()
	if err != nil {
		t.Fatal(err)
	}
	a := New(Args{RepoDir: repo, Template: *tmpl})
	_, preview, _, err := a.DryRun(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(preview) != 1 {
		t.Fatal(len(preview))
	}
	units, err := a.splitUnits(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	input := a.prepareReviewInput(t.Context(), units[0])
	messages := a.reviewMessages(units[0], input, "")
	unitreview.AttachMessages(&units[0], messages)
	if len(units[0].Review().RelatedDiffs) != 0 {
		t.Fatal("primary diff duplicated in Unit evidence")
	}
	projected := harness.ProjectContext(messages, msg.Artifacts(messages), true)
	expected := agentgo.ToMessages(projected)
	if len(expected) != len(preview[0].ProjectedMessages) {
		t.Fatal("preview diverged")
	}
	for i, m := range expected {
		if m.TextContent() != preview[0].ProjectedMessages[i].TextContent() {
			t.Fatalf("message %d differs", i)
		}
	}
	foundDiff := false
	for _, m := range messages {
		if _, ok := m.(*msg.Diff); ok {
			foundDiff = true
		}
		if _, ok := m.(msg.Instruction); ok && strings.Contains(m.TextContent(), "+func Value") {
			t.Fatal("diff still pinned inside task text")
		}
	}
	if !foundDiff || len(preview[0].Artifacts) == 0 || preview[0].EstimatedMessageTokens == 0 {
		t.Fatal("missing prepared materials")
	}
}
