package hypothesisreview

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
	DossierID        string            `json:"dossier_id,omitempty"`
	LaneID           string            `json:"lane_id,omitempty"`
	SubmissionIndex  int               `json:"submission_index,omitempty"`
}

var SubmitAssessments = tool.Named("submit_assessments")

// AssessmentSubmission is the append-only observation emitted whenever
// Review 2 accepts or replaces one judgment. Trial later consumes only the
// collector's latest value for each hypothesis.
type AssessmentSubmission struct {
	Assessment Assessment
	Replaced   bool
}

// AssessmentCollector is scoped to one Dossier execution.
type AssessmentCollector struct {
	mu          sync.Mutex
	assessments map[string]Assessment
	expected    map[string]bool
	nextIndex   int
}

func NewAssessmentCollector(expected ...string) *AssessmentCollector {
	want := make(map[string]bool, len(expected))
	for _, id := range expected {
		if id != "" {
			want[id] = true
		}
	}
	return &AssessmentCollector{assessments: make(map[string]Assessment), expected: want}
}

func (c *AssessmentCollector) Add(a Assessment) (AssessmentSubmission, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.expected) > 0 && !c.expected[a.HypothesisID] {
		return AssessmentSubmission{}, false
	}
	_, replaced := c.assessments[a.HypothesisID]
	c.nextIndex++
	a.SubmissionIndex = c.nextIndex
	c.assessments[a.HypothesisID] = a
	return AssessmentSubmission{Assessment: a, Replaced: replaced}, true
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

func (c *AssessmentCollector) Missing() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	missing := make([]string, 0, len(c.expected))
	for id := range c.expected {
		if _, ok := c.assessments[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

func (c *AssessmentCollector) Complete() bool { return len(c.Missing()) == 0 }

// AssessmentHook captures the result tool while every evidence-gathering tool
// continues through Harness's read-only Registry.
type AssessmentHook struct {
	Collector  *AssessmentCollector
	Evidence   *EvidenceLedger
	DossierID  string
	LaneID     string
	OnAccepted func(AssessmentSubmission)
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
	var accepted, replaced, rejected []string
	for _, assessment := range assessments {
		assessment.ReviewerAlias = request.Alias
		assessment.DossierID = h.DossierID
		assessment.LaneID = h.LaneID
		if h.Evidence != nil {
			assessment.EvidenceReceipts = h.Evidence.Receipts()
		}
		submission, ok := h.Collector.Add(assessment)
		if !ok {
			rejected = append(rejected, assessment.HypothesisID)
			continue
		}
		accepted = append(accepted, assessment.HypothesisID)
		if submission.Replaced {
			replaced = append(replaced, assessment.HypothesisID)
		}
		if h.OnAccepted != nil {
			h.OnAccepted(submission)
		}
	}
	result, _ := json.Marshal(map[string]any{
		"accepted":         accepted,
		"replaced":         replaced,
		"rejected_unknown": rejected,
		"remaining":        h.Collector.Missing(),
	})
	return tool.Of(string(result)), true
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

func AssessmentToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name: SubmitAssessments.Name(),
			Description: "Submit one or more completed hypothesis assessments as soon as they are decided. " +
				"Call this tool repeatedly for partial batches; later valid submissions replace earlier judgments for the same hypothesis. " +
				"This records review judgments for deterministic Trial and does not publish comments.",
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
