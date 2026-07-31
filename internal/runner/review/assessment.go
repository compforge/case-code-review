package review

import (
	"context"
	"encoding/json"
	"fmt"
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

type Actionability string

const (
	Actionable Actionability = "actionable"
	LowValue   Actionability = "low_value"
	Unknown    Actionability = "unknown"
)

// Assessment is the convergent Review's judgment of one Hypothesis. Support
// and Actionability stay orthogonal so a real but low-value issue is not
// mislabeled as factually wrong.
type Assessment struct {
	HypothesisID  string        `json:"hypothesis_id"`
	Support       Support       `json:"support"`
	Actionability Actionability `json:"actionability"`
	Reason        string        `json:"reason"`
	Evidence      []string      `json:"evidence,omitempty"`
	ReviewerAlias string        `json:"reviewer_alias,omitempty"`
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
	return out
}

// AssessmentHook captures the result tool while every evidence-gathering tool
// continues through Harness's read-only Registry.
type AssessmentHook struct {
	Collector *AssessmentCollector
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
			HypothesisID:  stringValue(item, "hypothesis_id"),
			Support:       Support(strings.ToLower(stringValue(item, "support"))),
			Actionability: Actionability(strings.ToLower(stringValue(item, "actionability"))),
			Reason:        stringValue(item, "reason"),
			Evidence:      stringSlice(item["evidence"]),
		}
		if assessment.HypothesisID == "" || assessment.Reason == "" ||
			!validSupport(assessment.Support) || !validActionability(assessment.Actionability) {
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

func validActionability(value Actionability) bool {
	return value == Actionable || value == LowValue || value == Unknown
}

// Trial is deliberately deterministic. Missing, contradicted, insufficient,
// unknown, and low-value assessments cannot become public Findings.
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
		if !ok || assessment.Support != Supported || assessment.Actionability != Actionable {
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
								"actionability": map[string]any{
									"type": "string",
									"enum": []string{string(Actionable), string(LowValue), string(Unknown)},
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
							"required": []string{"hypothesis_id", "support", "actionability", "reason", "evidence"},
						},
					},
				},
				"required": []string{"assessments"},
			},
		},
	}
}
