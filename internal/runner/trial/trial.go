// Package trial applies the deterministic delivery policy after Hypothesis
// Review. It performs no LLM calls and gathers no new evidence.
package trial

import (
	"sort"
	"sync"

	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/runner/hypothesisreview"
	"github.com/qiankunli/case-code-review/internal/runner/unitreview"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// Gate applies Trial one Hypothesis at a time as Review 2 results arrive. It
// retains already-delivered Findings when later reviews time out, while Results
// restores stable output ordering after concurrent execution.
type Gate struct {
	mu        sync.Mutex
	decided   map[string]bool
	delivered map[string]bool
	findings  []deliveredFinding
	decisions []unit.TrialDecision
}

type deliveredFinding struct {
	hypothesisID string
	finding      finding.Finding
}

func NewGate() *Gate {
	return &Gate{
		decided: make(map[string]bool), delivered: make(map[string]bool),
	}
}

// Passes is the single delivery policy. A model judgment is not enough: the
// reviewer must have read the hypothesis' diff, and all four gates must pass.
func Passes(hypothesis unit.Hypothesis, assessment unit.Assessment) bool {
	return assessment.Support == unit.Supported &&
		assessment.Attribution == unit.Caused &&
		assessment.Value == unit.Actionable &&
		assessment.Novelty == unit.Novel &&
		len(assessment.Evidence) > 0 &&
		!hasReceipt(assessment.EvidenceReceipts, hypothesisreview.ExternalEvidenceUnverifiedReceipt, hypothesis.ID) &&
		hasDiffReceipt(assessment.EvidenceReceipts, hypothesis.Path)
}

// Assess immediately applies Trial to one accepted Assessment. The bool is
// false when this Hypothesis occurrence was already decided.
func (g *Gate) Assess(
	reviewUnit unit.Unit,
	hypothesis unit.Hypothesis,
	assessment unit.Assessment,
) (finding.Finding, unit.TrialDecision, bool) {
	return g.evaluate(reviewUnit, hypothesis, assessment, true)
}

func (g *Gate) evaluate(
	reviewUnit unit.Unit,
	hypothesis unit.Hypothesis,
	assessment unit.Assessment,
	hasAssessment bool,
) (finding.Finding, unit.TrialDecision, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.decided[hypothesis.ID] {
		return finding.Finding{}, unit.TrialDecision{}, false
	}
	g.decided[hypothesis.ID] = true

	fingerprint := hypothesis.Fingerprint
	if fingerprint == "" {
		fingerprint = unitreview.FingerprintFor(hypothesis)
	}
	passed := hasAssessment && Passes(hypothesis, assessment)
	decision := unit.TrialDecision{HypothesisID: hypothesis.ID, Passed: passed}
	var delivered finding.Finding
	if passed && !g.delivered[fingerprint] {
		g.delivered[fingerprint] = true
		decision.Delivered = true
		delivered = unitreview.FindingFor(hypothesis)
		g.findings = append(g.findings, deliveredFinding{
			hypothesisID: hypothesis.ID, finding: delivered,
		})
	}
	reviewUnit.AddTrialDecision(decision)
	g.decisions = append(g.decisions, decision)
	return delivered, decision, true
}

// Finalize records deterministic rejection decisions for any Hypothesis that
// never reached Review 2, and catches accepted Assessments whose callback was
// interrupted before incremental Trial ran.
func (g *Gate) Finalize(units []unit.Unit) ([]finding.Finding, []unit.TrialDecision) {
	var findings []finding.Finding
	var decisions []unit.TrialDecision
	for _, entry := range trialEntries(units) {
		assessment, ok := entry.assessments[entry.hypothesis.ID]
		delivered, decision, fresh := g.evaluate(entry.unit, entry.hypothesis, assessment, ok)
		if fresh {
			decisions = append(decisions, decision)
			if decision.Delivered {
				findings = append(findings, delivered)
			}
		}
	}
	return findings, decisions
}

func (g *Gate) Results() ([]finding.Finding, []unit.TrialDecision) {
	g.mu.Lock()
	defer g.mu.Unlock()
	entries := append([]deliveredFinding(nil), g.findings...)
	decisions := append([]unit.TrialDecision(nil), g.decisions...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].hypothesisID < entries[j].hypothesisID })
	sort.Slice(decisions, func(i, j int) bool { return decisions[i].HypothesisID < decisions[j].HypothesisID })
	findings := make([]finding.Finding, len(entries))
	for i := range entries {
		findings[i] = entries[i].finding
	}
	return findings, decisions
}

// Run reads each Unit's complete review state and converts only approved,
// deduplicated hypotheses into public Findings. Trial is deterministic today,
// but receiving the aggregate root keeps all admitted facts available to
// future policy without another stage-to-stage transfer model.
func Run(units []unit.Unit) ([]finding.Finding, []unit.TrialDecision) {
	gate := NewGate()
	gate.Finalize(units)
	return gate.Results()
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
	return hasReceipt(receipts, "diff", path)
}

func hasReceipt(receipts []unit.EvidenceReceipt, kind, ref string) bool {
	for _, receipt := range receipts {
		if receipt.Kind == kind && receipt.Ref == ref {
			return true
		}
	}
	return false
}
