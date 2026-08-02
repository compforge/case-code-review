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
		Clues: []unit.Clue{{Kind: unit.ClueDoc, Relation: unit.RelCallee, Ref: "a.go::A", Text: "long clue body"}},
	}
	input.EvidencePaths = []string{"a.go", "shared/context.go"}
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
