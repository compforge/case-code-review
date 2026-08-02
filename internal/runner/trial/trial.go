// Package trial applies the deterministic delivery policy after Hypothesis
// Review. It performs no LLM calls and gathers no new evidence.
package trial

import (
	"sort"

	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// Passes is the single delivery policy. A model judgment is not enough: the
// reviewer must have read the hypothesis' diff, and all four gates must pass.
func Passes(hypothesis unit.Hypothesis, assessment unit.Assessment) bool {
	return assessment.Support == unit.Supported &&
		assessment.Attribution == unit.Caused &&
		assessment.Value == unit.Actionable &&
		assessment.Novelty == unit.Novel &&
		len(assessment.Evidence) > 0 &&
		hasDiffReceipt(assessment.EvidenceReceipts, hypothesis.Path)
}

// Run reads each Unit's complete review state and converts only approved,
// deduplicated hypotheses into public Findings. Trial is deterministic today,
// but receiving the aggregate root keeps all admitted facts available to
// future policy without another stage-to-stage transfer model.
func Run(units []unit.Unit) ([]finding.Finding, []unit.TrialDecision) {
	entries := trialEntries(units)
	delivered := make(map[string]bool, len(entries))
	out := make([]finding.Finding, 0, len(entries))
	decisions := make([]unit.TrialDecision, 0, len(entries))
	for _, entry := range entries {
		hypothesis := entry.hypothesis
		fingerprint := hypothesis.Fingerprint
		if fingerprint == "" {
			fingerprint = unitreview.FingerprintFor(hypothesis)
		}
		assessment, ok := entry.assessments[hypothesis.ID]
		passed := ok && Passes(hypothesis, assessment)
		decision := unit.TrialDecision{HypothesisID: hypothesis.ID, Passed: passed}
		if !passed || delivered[fingerprint] {
			entry.unit.AddTrialDecision(decision)
			decisions = append(decisions, decision)
			continue
		}
		delivered[fingerprint] = true
		decision.Delivered = true
		entry.unit.AddTrialDecision(decision)
		decisions = append(decisions, decision)
		out = append(out, unitreview.FindingFor(hypothesis))
	}
	return out, decisions
}

// Bypass preserves the former one-stage behavior for the feature-gate
// ablation; production delivery should use Run.
func Bypass(units []unit.Unit) ([]finding.Finding, []unit.TrialDecision) {
	entries := trialEntries(units)
	delivered := make(map[string]bool, len(entries))
	out := make([]finding.Finding, 0, len(entries))
	decisions := make([]unit.TrialDecision, 0, len(entries))
	for _, entry := range entries {
		hypothesis := entry.hypothesis
		fingerprint := hypothesis.Fingerprint
		if fingerprint == "" {
			fingerprint = unitreview.FingerprintFor(hypothesis)
		}
		decision := unit.TrialDecision{HypothesisID: hypothesis.ID, Passed: true}
		if delivered[fingerprint] {
			entry.unit.AddTrialDecision(decision)
			decisions = append(decisions, decision)
			continue
		}
		delivered[fingerprint] = true
		decision.Delivered = true
		entry.unit.AddTrialDecision(decision)
		decisions = append(decisions, decision)
		out = append(out, unitreview.FindingFor(hypothesis))
	}
	return out, decisions
}

type trialEntry struct {
	unit        unit.Unit
	hypothesis  unit.Hypothesis
	assessments map[string]unit.Assessment
}

func trialEntries(units []unit.Unit) []trialEntry {
	var entries []trialEntry
	for _, reviewUnit := range units {
		snapshot := reviewUnit.Review()
		assessments := make(map[string]unit.Assessment, len(snapshot.Assessments))
		for _, assessment := range snapshot.Assessments {
			assessments[assessment.HypothesisID] = assessment
		}
		for _, hypothesis := range snapshot.Hypotheses {
			entries = append(entries, trialEntry{
				unit: reviewUnit, hypothesis: hypothesis, assessments: assessments,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].hypothesis.ID < entries[j].hypothesis.ID })
	return entries
}

func hasDiffReceipt(receipts []unit.EvidenceReceipt, path string) bool {
	for _, receipt := range receipts {
		if receipt.Kind == "diff" && receipt.Ref == path {
			return true
		}
	}
	return false
}
