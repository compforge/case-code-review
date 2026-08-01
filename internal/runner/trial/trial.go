// Package trial applies the deterministic delivery policy after Hypothesis
// Review. It performs no LLM calls and gathers no new evidence.
package trial

import (
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
)

// Passes is the single delivery policy. A model judgment is not enough: the
// reviewer must have read the hypothesis' diff, and all four gates must pass.
func Passes(hypothesis unitreview.Hypothesis, assessment hypothesisreview.Assessment) bool {
	return assessment.Support == hypothesisreview.Supported &&
		assessment.Attribution == hypothesisreview.Caused &&
		assessment.Value == hypothesisreview.Actionable &&
		assessment.Novelty == hypothesisreview.Novel &&
		len(assessment.Evidence) > 0 &&
		hasDiffReceipt(assessment.EvidenceReceipts, hypothesis.Path)
}

// Run converts only approved, deduplicated hypotheses into public Findings.
func Run(
	hypotheses []unitreview.Hypothesis,
	assessments []hypothesisreview.Assessment,
) []finding.Finding {
	byID := make(map[string]hypothesisreview.Assessment, len(assessments))
	for _, assessment := range assessments {
		byID[assessment.HypothesisID] = assessment
	}
	seen := make(map[string]bool, len(hypotheses))
	out := make([]finding.Finding, 0, len(hypotheses))
	for _, hypothesis := range hypotheses {
		if seen[hypothesis.ID] {
			continue
		}
		seen[hypothesis.ID] = true
		assessment, ok := byID[hypothesis.ID]
		if !ok || !Passes(hypothesis, assessment) {
			continue
		}
		out = append(out, hypothesis.Finding())
	}
	return out
}

// Bypass preserves the former one-stage behavior for the feature-gate
// ablation; production delivery should use Run.
func Bypass(hypotheses []unitreview.Hypothesis) []finding.Finding {
	seen := make(map[string]bool, len(hypotheses))
	out := make([]finding.Finding, 0, len(hypotheses))
	for _, hypothesis := range hypotheses {
		if seen[hypothesis.ID] {
			continue
		}
		seen[hypothesis.ID] = true
		out = append(out, hypothesis.Finding())
	}
	return out
}

func hasDiffReceipt(receipts []hypothesisreview.EvidenceReceipt, path string) bool {
	for _, receipt := range receipts {
		if receipt.Kind == "diff" && receipt.Ref == path {
			return true
		}
	}
	return false
}
