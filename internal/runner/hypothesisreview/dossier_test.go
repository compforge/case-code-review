package hypothesisreview

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestDossierMessageCompactionKeepsEveryHypothesis(t *testing.T) {
	dossier := Dossier{
		ID: "d-1",
		Hypotheses: []unitreview.Hypothesis{
			{ID: "h-1", Path: "a.go", Trigger: "trigger one", Impact: "impact one"},
			{ID: "h-2", Path: "b.go", Trigger: "trigger two", Impact: "impact two"},
		},
		Clues: []unit.Clue{{Kind: unit.ClueDoc, Relation: unit.RelCallee, Ref: "a.go::A", Text: "long clue body"}},
	}
	dossier.EvidencePaths = []string{"a.go", "shared/context.go"}
	template := "hypotheses={{hypotheses}} clues={{clues}} paths={{evidence_paths}} prior={{prior_assessments}}"
	full := renderDossierPrompt(template, Config{}, dossier, false)
	condensed := renderDossierPrompt(template, Config{}, dossier, true)
	message := newDossierMessage(full, condensed)

	lowered := message.ToLLM(msg.CompactionCondensed)
	compact := lowered.ExtractText()
	for _, required := range []string{"h-1", "trigger one", "h-2", "trigger two"} {
		if !strings.Contains(compact, required) {
			t.Fatalf("compacted dossier dropped %q: %s", required, compact)
		}
	}
	if strings.Contains(compact, "long clue body") {
		t.Fatalf("compacted dossier retained full clue text: %s", compact)
	}
	if !strings.Contains(compact, "shared/context.go") {
		t.Fatalf("compacted dossier dropped evidence paths: %s", compact)
	}
	if message.MaxCompaction() != msg.CompactionCondensed {
		t.Fatalf("Dossier must not compact below complete hypotheses")
	}
}
