package review

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
)

type Support string

const (
	Supported    Support = "supported"
	Contradicted Support = "contradicted"
	Insufficient Support = "insufficient"
)

type Attribution string

const (
	// Caused is factual causation, not intent or blame: reverting the current
	// diff would remove or materially change the hypothesis' trigger or impact.
	Caused             Attribution = "caused"
	PreExisting        Attribution = "pre_existing"
	AttributionUnknown Attribution = "unknown"
)

type DeliveryValue string

const (
	Actionable   DeliveryValue = "actionable"
	LowValue     DeliveryValue = "low_value"
	ValueUnknown DeliveryValue = "unknown"
)

type Novelty string

const (
	Novel            Novelty = "new"
	DuplicateInCase  Novelty = "duplicate_in_case"
	AlreadyDelivered Novelty = "already_delivered"
)

// Assessment is the convergent Review's judgment of one Hypothesis. Support
// answers whether the claim is true; attribution, value, and novelty separately
// answer whether this review should deliver it. EvidenceReceipts are populated
// by Runner from actual read-only tool executions, never trusted model text.
type Assessment struct {
	HypothesisID     string            `json:"hypothesis_id"`
	Support          Support           `json:"support"`
	Attribution      Attribution       `json:"attribution"`
	Value            DeliveryValue     `json:"value"`
	Novelty          Novelty           `json:"novelty"`
	Reason           string            `json:"reason"`
	Evidence         []string          `json:"evidence,omitempty"`
	EvidenceReceipts []EvidenceReceipt `json:"evidence_receipts,omitempty"`
	ReviewerAlias    string            `json:"reviewer_alias,omitempty"`
}

var SubmitAssessments = tool.Named("submit_assessments")

const AssessmentSubmitted = "Assessments recorded for Trial."

// AssessmentCollector is scoped to one run-level Hypothesis Review.
type AssessmentCollector struct {
	mu          sync.Mutex
	assessments map[string]Assessment
}

func NewAssessmentCollector() *AssessmentCollector {
	return &AssessmentCollector{assessments: make(map[string]Assessment)}
}

func (c *AssessmentCollector) Add(a Assessment) {
	c.mu.Lock()
	c.assessments[a.HypothesisID] = a
	c.mu.Unlock()
}

func (c *AssessmentCollector) Assessments() []Assessment {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Assessment, 0, len(c.assessments))
	for _, assessment := range c.assessments {
		out = append(out, assessment)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].HypothesisID < out[j].HypothesisID })
	return out
}

// AssessmentHook captures the result tool while every evidence-gathering tool
// continues through Harness's read-only Registry.
type AssessmentHook struct {
	Collector *AssessmentCollector
	Evidence  *EvidenceLedger
}

func (h *AssessmentHook) HandleTool(
	_ context.Context,
	request harness.ToolRequest,
) (tool.TaskCheckpoint, bool) {
	if request.Tool != SubmitAssessments {
		return tool.TaskCheckpoint{}, false
	}
	assessments, errMsg := ParseAssessments(request.Args)
	if errMsg != "" {
		return tool.Of(errMsg), true
	}
	for _, assessment := range assessments {
		assessment.ReviewerAlias = request.Alias
		if h.Evidence != nil {
			assessment.EvidenceReceipts = h.Evidence.Receipts()
		}
		h.Collector.Add(assessment)
	}
	return tool.Of(AssessmentSubmitted), true
}

func ParseAssessments(args map[string]any) ([]Assessment, string) {
	var rawItems []any
	switch raw := args["assessments"].(type) {
	case []any:
		rawItems = raw
	case string:
		if err := json.Unmarshal([]byte(raw), &rawItems); err != nil {
			return nil, fmt.Sprintf("Error: failed to parse 'assessments' JSON string: %v", err)
		}
	}
	if len(rawItems) == 0 {
		return nil, "Error: 'assessments' array is required"
	}

	out := make([]Assessment, 0, len(rawItems))
	for _, raw := range rawItems {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		assessment := Assessment{
			HypothesisID: stringValue(item, "hypothesis_id"),
			Support:      Support(strings.ToLower(stringValue(item, "support"))),
			Attribution:  Attribution(strings.ToLower(stringValue(item, "attribution"))),
			Value:        DeliveryValue(strings.ToLower(stringValue(item, "value"))),
			Novelty:      Novelty(strings.ToLower(stringValue(item, "novelty"))),
			Reason:       stringValue(item, "reason"),
			Evidence:     stringSlice(item["evidence"]),
		}
		if assessment.HypothesisID == "" || assessment.Reason == "" ||
			!validSupport(assessment.Support) || !validAttribution(assessment.Attribution) ||
			!validValue(assessment.Value) || !validNovelty(assessment.Novelty) ||
			(assessment.Support != Insufficient && len(assessment.Evidence) == 0) {
			continue
		}
		out = append(out, assessment)
	}
	if len(out) == 0 {
		return nil, "Error: no valid assessment was provided"
	}
	return out, ""
}

func validSupport(value Support) bool {
	return value == Supported || value == Contradicted || value == Insufficient
}

func validAttribution(value Attribution) bool {
	return value == Caused || value == PreExisting || value == AttributionUnknown
}

func validValue(value DeliveryValue) bool {
	return value == Actionable || value == LowValue || value == ValueUnknown
}

func validNovelty(value Novelty) bool {
	return value == Novel || value == DuplicateInCase || value == AlreadyDelivered
}

// PassesTrial is the single delivery policy. A model judgment is not enough:
// the reviewer must have actually read the hypothesis' diff, and all four
// independent gates must pass.
func PassesTrial(hypothesis Hypothesis, assessment Assessment) bool {
	return assessment.Support == Supported &&
		assessment.Attribution == Caused &&
		assessment.Value == Actionable &&
		assessment.Novelty == Novel &&
		len(assessment.Evidence) > 0 &&
		hasDiffReceipt(assessment.EvidenceReceipts, hypothesis.Path)
}

// Trial is deliberately deterministic. Missing evidence receipts, unsupported
// claims, pre-existing behavior, low-value observations, and repeated delivery
// cannot become public Findings.
func Trial(hypotheses []Hypothesis, assessments []Assessment) []finding.Finding {
	byID := make(map[string]Assessment, len(assessments))
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
		if !ok || !PassesTrial(hypothesis, assessment) {
			continue
		}
		out = append(out, hypothesis.Finding())
	}
	return out
}

// BypassTrial is used only when the hypothesis_review feature gate is disabled
// for ablation. It preserves the former one-stage delivery behavior.
func BypassTrial(hypotheses []Hypothesis) []finding.Finding {
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

func AssessmentToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name: SubmitAssessments.Name(),
			Description: "Submit the independent assessment of every supplied hypothesis. " +
				"This records review judgments for deterministic Trial; it does not publish comments.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"assessments": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"hypothesis_id": map[string]any{"type": "string"},
								"support": map[string]any{
									"type": "string",
									"enum": []string{string(Supported), string(Contradicted), string(Insufficient)},
								},
								"attribution": map[string]any{
									"type": "string",
									"enum": []string{string(Caused), string(PreExisting), string(AttributionUnknown)},
								},
								"value": map[string]any{
									"type": "string",
									"enum": []string{string(Actionable), string(LowValue), string(ValueUnknown)},
								},
								"novelty": map[string]any{
									"type": "string",
									"enum": []string{string(Novel), string(DuplicateInCase), string(AlreadyDelivered)},
								},
								"reason": map[string]any{
									"type":        "string",
									"description": "Concise case-specific reasoning, including the decisive support, counter-evidence, or missing fact.",
								},
								"evidence": map[string]any{
									"type":        "array",
									"items":       map[string]any{"type": "string"},
									"description": "Source locations, contracts, or tool observations actually checked.",
								},
							},
							"required": []string{"hypothesis_id", "support", "attribution", "value", "novelty", "reason", "evidence"},
						},
					},
				},
				"required": []string{"assessments"},
			},
		},
	}
}
