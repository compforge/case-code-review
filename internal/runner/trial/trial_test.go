package trial

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestRunRequiresEveryDeliveryGateAndDiffReceipt(t *testing.T) {
	approved := unitreview.Hypothesis{Path: "a.go", Content: "real issue", ExistingCode: "x"}
	approved.ID = unitreview.IDFor(approved)
	lowValue := unitreview.Hypothesis{Path: "a.go", Content: "minor issue", ExistingCode: "y"}
	lowValue.ID = unitreview.IDFor(lowValue)
	missing := unitreview.Hypothesis{Path: "a.go", Content: "unreviewed", ExistingCode: "z"}
	missing.ID = unitreview.IDFor(missing)

	reviewUnit := trialUnit("u-1",
		[]unitreview.Hypothesis{approved, lowValue, missing},
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
		})
	findings, decisions := Run([]unit.Unit{reviewUnit})
	if len(findings) != 1 || findings[0].Content != approved.Content {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	if len(decisions) != 3 {
		t.Fatalf("decisions = %+v, want one per Hypothesis occurrence", decisions)
	}
	if got := reviewUnit.Review().Decisions; len(got) != 3 {
		t.Fatalf("Trial decisions were not attached to Unit: %+v", got)
	}
}

func TestRunDeduplicatesClaimFingerprintWithoutLosingUnitOccurrences(t *testing.T) {
	left := unitreview.Hypothesis{OriginUnit: "u-left", Path: "a.go", Content: "same issue", ExistingCode: "x"}
	right := left
	right.OriginUnit = "u-right"
	left.Fingerprint = unit.HypothesisFingerprintFor(left)
	right.Fingerprint = unit.HypothesisFingerprintFor(right)
	left.ID = unit.HypothesisIDFor(left)
	right.ID = unit.HypothesisIDFor(right)

	assessmentFor := func(id string) hypothesisreview.Assessment {
		return hypothesisreview.Assessment{
			HypothesisID: id, Support: hypothesisreview.Supported,
			Attribution: hypothesisreview.Caused, Value: hypothesisreview.Actionable,
			Novelty: hypothesisreview.Novel, Evidence: []string{"a.go:1"},
			EvidenceReceipts: []hypothesisreview.EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
		}
	}
	leftUnit := trialUnit("u-left", []unitreview.Hypothesis{left}, []hypothesisreview.Assessment{assessmentFor(left.ID)})
	rightUnit := trialUnit("u-right", []unitreview.Hypothesis{right}, []hypothesisreview.Assessment{assessmentFor(right.ID)})
	findings, decisions := Run([]unit.Unit{leftUnit, rightUnit})
	if left.ID == right.ID || left.Fingerprint != right.Fingerprint {
		t.Fatalf("invalid occurrence identities: left=%+v right=%+v", left, right)
	}
	if len(findings) != 1 || len(decisions) != 2 || !decisions[0].Delivered || decisions[1].Delivered {
		t.Fatalf("findings=%+v decisions=%+v", findings, decisions)
	}
}

func TestGateDecidesFindingBeforeTheRunFinishes(t *testing.T) {
	hypothesis := unitreview.Hypothesis{
		OriginUnit: "u-1", Path: "a.go", Content: "real issue", ExistingCode: "x",
		Trigger: "call", Impact: "failure", ChangeAttribution: "changed",
	}
	hypothesis.Fingerprint = unitreview.FingerprintFor(hypothesis)
	hypothesis.ID = unitreview.IDFor(hypothesis)
	reviewUnit := trialUnit("u-1", []unitreview.Hypothesis{hypothesis}, nil)
	assessment := hypothesisreview.Assessment{
		HypothesisID: hypothesis.ID, Support: hypothesisreview.Supported,
		Attribution: hypothesisreview.Caused, Value: hypothesisreview.Actionable,
		Novelty: hypothesisreview.Novel, Evidence: []string{"a.go:1"},
		EvidenceReceipts: []hypothesisreview.EvidenceReceipt{{Kind: "diff", Ref: "a.go"}},
	}

	gate := NewGate()
	delivered, decision, fresh := gate.Assess(reviewUnit, hypothesis, assessment)
	if !fresh || !decision.Delivered || delivered.Content != hypothesis.Content {
		t.Fatalf("delivered=%+v decision=%+v fresh=%v", delivered, decision, fresh)
	}
	findings, decisions := gate.Results()
	if len(findings) != 1 || len(decisions) != 1 {
		t.Fatalf("findings=%+v decisions=%+v", findings, decisions)
	}
}

func trialUnit(id string, hypotheses []unitreview.Hypothesis, assessments []hypothesisreview.Assessment) unit.Unit {
	reviewUnit := unit.Unit{ID: id}
	reviewUnit.InitReviewState()
	for _, hypothesis := range hypotheses {
		reviewUnit.AddHypothesis(hypothesis)
	}
	for _, assessment := range assessments {
		reviewUnit.AddAssessment(assessment)
	}
	return reviewUnit
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
