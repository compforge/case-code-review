package hypothesisreview

import (
	"encoding/json"
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

const WrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop gathering evidence now. " +
	"Submit the current hypothesis assessment using only the evidence already gathered. " +
	"A valid submit_assessment call ends this review. Use insufficient/unknown when a decisive fact is still missing; do not " +
	"claim support without the required diff evidence receipt."

var CheckExternalEvidence = tool.Named("check_external_evidence")

const ExternalEvidenceUnverifiedReceipt = "external_unverified"

// ToolDefs is a structural allowlist: Hypothesis Review can inspect code and
// submit assessments, but cannot publish findings or propose new hypotheses.
func ToolDefs(main []llm.ToolDef) []llm.ToolDef {
	allowed := map[string]bool{
		tool.FileRead.Name():     true,
		tool.FileReadBase.Name(): true,
		tool.FileFind.Name():     true,
		tool.FileReadDiff.Name(): true,
		tool.CodeSearch.Name():   true,
	}
	out := make([]llm.ToolDef, 0, len(main)+2)
	for _, def := range main {
		if allowed[def.Function.Name] {
			out = append(out, def)
		}
	}
	out = append(out, ExternalEvidenceToolDef(), AssessmentToolDef())
	return out
}

// ExternalEvidenceToolDef exposes CCR's evidence boundary as a tool affordance.
// It does not pretend to browse the internet: the successful result means this
// run has no authoritative external contract evidence for the premise.
func ExternalEvidenceToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name: CheckExternalEvidence.Name(),
			Description: "Use when a decisive premise depends on behavior owned by an external provider, SDK, API, database, or protocol and is not proven by retained repository evidence. " +
				"This records the evidence boundary without performing network research; an unverified result requires support=insufficient.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"claim": map[string]any{
						"type":        "string",
						"description": "The decisive external behavior premise. Optional because the current Hypothesis is already bound to this Review 2 execution.",
					},
				},
			},
		},
	}
}

func externalEvidenceResult(
	requestID string,
	hypothesis unit.Hypothesis,
	args map[string]any,
) (string, EvidenceReceipt) {
	claim := strings.TrimSpace(stringValue(args, "claim"))
	if claim == "" {
		claim = strings.TrimSpace(hypothesis.Trigger)
	}
	if claim == "" {
		claim = strings.TrimSpace(hypothesis.Content)
	}
	receipt := EvidenceReceipt{
		ToolCallID: requestID,
		Kind:       ExternalEvidenceUnverifiedReceipt,
		Ref:        hypothesis.ID,
	}
	result, _ := json.Marshal(map[string]any{
		"status":           "unverified",
		"claim":            claim,
		"message":          "CCR has no authoritative external contract evidence for this premise in the current run.",
		"required_support": "insufficient",
		"receipt":          receipt,
	})
	return string(result), receipt
}
