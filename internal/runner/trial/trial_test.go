package trial

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
)

func TestRunRequiresEveryDeliveryGateAndDiffReceipt(t *testing.T) {
	approved := unitreview.Hypothesis{Path: "a.go", Content: "real issue", ExistingCode: "x"}
	approved.ID = unitreview.IDFor(approved)
	lowValue := unitreview.Hypothesis{Path: "a.go", Content: "minor issue", ExistingCode: "y"}
	lowValue.ID = unitreview.IDFor(lowValue)
	missing := unitreview.Hypothesis{Path: "a.go", Content: "unreviewed", ExistingCode: "z"}
	missing.ID = unitreview.IDFor(missing)

	findings := Run(
		[]unitreview.Hypothesis{approved, approved, lowValue, missing},
		[]hypothesisreview.Assessment{
			{
				HypothesisID: approved.ID, Support: hypothesisreview.Supported,
				Attribution: hypothesisreview.Caused, Value: hypothesisreview.Actionable, Novelty: hypothesisreview.Novel,
				Evidence:         []string{"a.go:10"},
				EvidenceReceipts: []hypothesisreview.EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
			},
			{
				HypothesisID: lowValue.ID, Support: hypothesisreview.Supported,
				Attribution: hypothesisreview.Caused, Value: hypothesisreview.LowValue, Novelty: hypothesisreview.Novel,
				Evidence:         []string{"a.go:20"},
				EvidenceReceipts: []hypothesisreview.EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
			},
		},
	)
	if len(findings) != 1 || findings[0].Content != approved.Content {
		t.Fatalf("unexpected findings: %+v", findings)
	}
}

func TestPassesRejectsModelEvidenceWithoutReceipt(t *testing.T) {
	hypothesis := unitreview.Hypothesis{Path: "a.go"}
	assessment := hypothesisreview.Assessment{
		Support: hypothesisreview.Supported, Attribution: hypothesisreview.Caused,
		Value: hypothesisreview.Actionable, Novelty: hypothesisreview.Novel,
		Evidence: []string{"a.go:10"},
	}
	if Passes(hypothesis, assessment) {
		t.Fatal("model-authored evidence must not replace a CCR receipt")
	}
	assessment.EvidenceReceipts = []hypothesisreview.EvidenceReceipt{{Kind: "diff", Ref: "a.go"}}
	if !Passes(hypothesis, assessment) {
		t.Fatal("complete assessment with a matching diff receipt should pass")
	}
}
