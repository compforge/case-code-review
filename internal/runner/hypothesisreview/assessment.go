package hypothesisreview

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

type Support = unit.Support

const (
	Supported    = unit.Supported
	Contradicted = unit.Contradicted
	Insufficient = unit.Insufficient
)

type Attribution = unit.Attribution

const (
	Caused             = unit.Caused
	PreExisting        = unit.PreExisting
	AttributionUnknown = unit.AttributionUnknown
)

type DeliveryValue = unit.DeliveryValue

const (
	Actionable   = unit.Actionable
	LowValue     = unit.LowValue
	ValueUnknown = unit.ValueUnknown
)

type Novelty = unit.Novelty

const (
	Novel            = unit.Novel
	DuplicateInCase  = unit.DuplicateInCase
	AlreadyDelivered = unit.AlreadyDelivered
)

// Assessment is the convergent Review's judgment of one Hypothesis. Support
// answers whether the claim is true; attribution, value, and novelty separately
// answer whether this review should deliver it. EvidenceReceipts are populated
// by Runner from actual read-only tool executions, never trusted model text.
type Assessment = unit.Assessment

var SubmitAssessment = tool.Named("submit_assessment")

// AssessmentSubmission is the append-only observation emitted whenever
// Review 2 accepts or replaces its one judgment. Trial later consumes the
// collector's latest value.
type AssessmentSubmission struct {
	Assessment Assessment
	Replaced   bool
}

// AssessmentCollector is scoped to one Hypothesis Review execution.
type AssessmentCollector struct {
	mu           sync.Mutex
	hypothesisID string
	assessment   *Assessment
	nextIndex    int
}

func NewAssessmentCollector(hypothesisID string) *AssessmentCollector {
	return &AssessmentCollector{hypothesisID: hypothesisID}
}

func (c *AssessmentCollector) Add(a Assessment) AssessmentSubmission {
	c.mu.Lock()
	defer c.mu.Unlock()
	a.HypothesisID = c.hypothesisID
	replaced := c.assessment != nil
	c.nextIndex++
	a.SubmissionIndex = c.nextIndex
	c.assessment = &a
	return AssessmentSubmission{Assessment: a, Replaced: replaced}
}

func (c *AssessmentCollector) Assessments() []Assessment {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.assessment == nil {
		return nil
	}
	return []Assessment{*c.assessment}
}

func (c *AssessmentCollector) Complete() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.assessment != nil
}

// AssessmentHook captures the result tool while every evidence-gathering tool
// continues through Harness's read-only Registry.
type AssessmentHook struct {
	Collector  *AssessmentCollector
	Evidence   *EvidenceLedger
	LaneID     string
	OnAccepted func(AssessmentSubmission)
}

func (h *AssessmentHook) HandleTool(
	_ context.Context,
	request harness.ToolRequest,
) (tool.TaskCheckpoint, bool) {
	if request.Tool != SubmitAssessment {
		return tool.TaskCheckpoint{}, false
	}
	assessment, errMsg := ParseAssessment(request.Args)
	if errMsg != "" {
		return tool.Of(errMsg), true
	}
	submission := h.Accept(assessment, request.Alias)
	result, _ := json.Marshal(map[string]any{
		"accepted": submission.Assessment.HypothesisID,
		"replaced": submission.Replaced,
	})
	return tool.CompleteWith(string(result)), true
}

// Accept attaches execution-owned identity and receipts instead of asking the
// model to copy them through tool arguments.
func (h *AssessmentHook) Accept(assessment Assessment, alias string) AssessmentSubmission {
	assessment.ReviewerAlias = alias
	assessment.LaneID = h.LaneID
	if h.Evidence != nil {
		assessment.EvidenceReceipts = h.Evidence.Receipts()
	}
	submission := h.Collector.Add(assessment)
	if h.OnAccepted != nil {
		h.OnAccepted(submission)
	}
	return submission
}

func ParseAssessment(args map[string]any) (Assessment, string) {
	assessment := Assessment{
		Support:     Support(strings.ToLower(stringValue(args, "support"))),
		Attribution: Attribution(strings.ToLower(stringValue(args, "attribution"))),
		Value:       DeliveryValue(strings.ToLower(stringValue(args, "value"))),
		Novelty:     Novelty(strings.ToLower(stringValue(args, "novelty"))),
		Reason:      stringValue(args, "reason"),
		Evidence:    stringSlice(args["evidence"]),
	}
	if assessment.Reason == "" || !validSupport(assessment.Support) ||
		!validAttribution(assessment.Attribution) || !validValue(assessment.Value) ||
		!validNovelty(assessment.Novelty) ||
		(assessment.Support != Insufficient && len(assessment.Evidence) == 0) {
		return Assessment{}, "Error: assessment is incomplete or has an invalid axis; non-insufficient support also requires evidence"
	}
	return assessment, ""
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
			Name: SubmitAssessment.Name(),
			Description: "Submit the completed assessment for the current hypothesis when the decision is ready. " +
				"A valid submission records the judgment for deterministic Trial and ends this Review 2 execution; it does not publish comments.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"support": map[string]any{
						"type":        "string",
						"enum":        []string{string(Supported), string(Contradicted), string(Insufficient)},
						"description": "Use insufficient when a decisive fact is unavailable, including behavior owned only by an external provider, SDK, or API.",
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
						"description": "Repository locations or tool observations actually checked; may be empty for insufficient support.",
					},
				},
				"required": []string{"support", "attribution", "value", "novelty", "reason", "evidence"},
			},
		},
	}
}
