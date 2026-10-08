package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestClueDocumentsLeaveFixedInstructionsAndShareBody(t *testing.T) {
	body := strings.Repeat("Preserve the caller's cancellation and return errors without losing their cause. ", 25)
	clues := []unit.Clue{
		{Kind: unit.ClueSpec, Relation: unit.RelSelf, Ref: "service.go::Run", Text: "existing contract"},
		{Kind: unit.ClueDoc, Relation: unit.RelCaller, Ref: "caller.go::Handle", Text: body},
		{Kind: unit.ClueDoc, Relation: unit.RelCallee, Ref: "callee.go::Invoke", Text: body},
		{Kind: unit.ClueDoc, Relation: unit.RelUsed, Ref: "types.go::Worker", Text: body},
	}
	inline, documents := separateClueDocuments(clues)
	specs, _, _, _ := renderClues(inline)
	if strings.Contains(specs, body) || !strings.Contains(specs, "existing contract") {
		t.Fatal("doc body remains pinned or spec was lost")
	}
	if len(documents) != 3 || clues[1].Text != body {
		t.Fatal("lost clue identity or changed Unit facts")
	}
	for _, document := range documents {
		if _, ok := document.(interface{ FixedContext() agentgo.AgentMessage }); ok {
			t.Fatal("doc is a fixed instruction")
		}
	}
	stop := errors.New("projection-only")
	agent := agentgo.NewAgent(agentgo.WithBeforeRun(func(_ context.Context, run agentgo.BeforeRunContext) error {
		if err := msg.RegisterArtifacts(run.Artifacts, documents); err != nil {
			return err
		}
		projected := msg.TransformMaterials(agentgo.TransformContext{Messages: documents, Artifacts: run.Artifacts})
		var content strings.Builder
		for _, message := range projected {
			content.WriteString(message.TextContent())
			content.WriteByte('\n')
		}
		if strings.Count(content.String(), body) != 1 {
			t.Fatal("ClueDoc body appears more than once")
		}
		for _, clue := range clues[1:] {
			if !strings.Contains(content.String(), clue.Ref) || !strings.Contains(content.String(), string(clue.Relation)) {
				t.Fatalf("lost clue provenance: %+v", clue)
			}
		}
		original, _, _, _ := renderClues(clues)
		projectedText := specs + "\n" + content.String()
		oldTokens, newTokens := llm.CountTokens(original), llm.CountTokens(projectedText)
		if newTokens >= oldTokens {
			t.Fatalf("no reduction: old=%d new=%d", oldTokens, newTokens)
		}
		t.Logf("synthetic repeated ClueDoc fixture: before=%d after=%d estimated tokens (%.1f%% reduction)", oldTokens, newTokens, 100*float64(oldTokens-newTokens)/float64(oldTokens))
		return stop
	}))
	if err := agent.Prompt(t.Context(), "review"); !errors.Is(err, stop) {
		t.Fatal(err)
	}
}
