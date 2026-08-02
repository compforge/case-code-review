package hypothesisreview

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestHypothesisMessageCompactionKeepsClaim(t *testing.T) {
	input := ReviewInput{
		Hypothesis: unitreview.Hypothesis{
			ID: "h-1", Path: "a.go", Trigger: "trigger one", Impact: "impact one",
		},
		Unit: unit.Unit{Clues: []unit.Clue{{Kind: unit.ClueDoc, Relation: unit.RelCallee, Ref: "a.go::A", Text: "long clue body"}}},
	}
	input.Unit.InitReviewState()
	input.Unit.AddFileSnapshot(unit.FileSnapshot{Kind: unit.CurrentSnapshot, Path: "shared/context.go", Content: "context"})
	template := "hypothesis={{hypothesis}} clues={{clues}} paths={{evidence_paths}} prior={{prior_assessments}}"
	full := renderReviewPrompt(template, Config{}, input, false, false)
	condensed := renderReviewPrompt(template, Config{}, input, true, false)
	message := newHypothesisMessage(full, condensed)

	lowered := message.ToLLM(msg.CompactionCondensed)
	compact := lowered.ExtractText()
	for _, required := range []string{"h-1", "trigger one"} {
		if !strings.Contains(compact, required) {
			t.Fatalf("compacted input dropped %q: %s", required, compact)
		}
	}
	if strings.Contains(compact, "long clue body") {
		t.Fatalf("compacted input retained full clue text: %s", compact)
	}
	if !strings.Contains(compact, "shared/context.go") {
		t.Fatalf("compacted input dropped evidence paths: %s", compact)
	}
	if message.MaxCompaction() != msg.CompactionCondensed {
		t.Fatalf("Review input must not compact below the complete hypothesis")
	}
}

func TestReviewChangeSetKeepsUnitDiffStatus(t *testing.T) {
	reviewUnit := unit.UnitOf(unit.Fragment{Path: "new.go", Status: "added", Insertions: 3})
	changes := reviewChangeSet(reviewUnit)
	if len(changes) != 1 || changes[0].Status != "added" {
		t.Fatalf("review change set = %+v, want added status", changes)
	}
}

func TestReviewContextMessagesCompactIndependentlyWithoutChangingUnitSnapshots(t *testing.T) {
	reviewUnit := unit.UnitOf(unit.Fragment{Path: "target.go", Diff: "@@ -1 +1 @@\n-old\n+new"})
	reviewUnit.AddFileSnapshot(unit.FileSnapshot{
		Kind: unit.CurrentSnapshot, Path: "source.go", Start: 1, End: 1, Total: 1,
		Content: "File: source.go (Total lines: 1)\n1|source body",
	})
	reviewUnit.AddRelatedDiff(unit.DiffSnapshot{
		Paths: []string{"related.go"}, Content: "@@ -2 +2 @@\n-old related\n+new related",
	})
	reviewUnit.AddSearchResult(unit.SearchResult{
		Kind: unit.CodeSearch, Query: "Call", Paths: []string{"caller.go"}, Content: "File: caller.go\nMatch lines: 3",
	})

	messages := reviewContextMessages(ReviewInput{Unit: reviewUnit})
	if len(messages) != 4 {
		t.Fatalf("context messages = %d, want target diff + file + related diff + search", len(messages))
	}
	wantPriorities := []int{priorityTargetDiff, priorityFile, priorityRelatedDiff, prioritySearch}
	for i, message := range messages {
		prioritized, ok := message.(msg.Prioritized)
		if !ok || prioritized.Priority() != wantPriorities[i] {
			t.Fatalf("message %d priority = %v, want %d", i, prioritized, wantPriorities[i])
		}
		compactable, ok := message.(msg.Compactable)
		if !ok || compactable.MaxCompaction() != msg.CompactionReference {
			t.Fatalf("message %d does not own reference compaction: %T", i, message)
		}
	}
	if got := reviewUnit.Review().FileSnapshots[0].Content; !strings.Contains(got, "source body") {
		t.Fatalf("message projection changed the Unit's raw file snapshot: %q", got)
	}
}
