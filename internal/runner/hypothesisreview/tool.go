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

var (
	WebSearch = tool.Named("web_search")
	WebFetch  = tool.Named("web_fetch")
)

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
	out := make([]llm.ToolDef, 0, len(main)+3)
	for _, def := range main {
		if allowed[def.Function.Name] {
			out = append(out, def)
		}
	}
	out = append(out, WebSearchToolDef(), WebFetchToolDef(), AssessmentToolDef())
	return out
}

// WebSearchToolDef exposes web discovery while keeping the current runtime's
// unavailable external-access boundary explicit to the reviewer.
func WebSearchToolDef() llm.ToolDef {
	return externalEvidenceToolDef(
		WebSearch,
		"Search the public web when a decisive premise requires discovering an external source. "+
			"External access is unavailable in this Review 2 runtime, so the result is unverified and requires support=insufficient.",
		"query",
		"The search query needed to verify the decisive external premise.",
	)
}

// WebFetchToolDef exposes retrieval of a known URL under the same boundary.
func WebFetchToolDef() llm.ToolDef {
	return externalEvidenceToolDef(
		WebFetch,
		"Fetch a known external URL when its content is decisive to the current hypothesis. "+
			"External access is unavailable in this Review 2 runtime, so the result is unverified and requires support=insufficient.",
		"url",
		"The external URL whose content is needed to verify the decisive premise.",
	)
}

func externalEvidenceToolDef(
	identity tool.Tool,
	description string,
	argument string,
	argumentDescription string,
) llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name:        identity.Name(),
			Description: description,
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					argument: map[string]any{
						"type":        "string",
						"description": argumentDescription,
					},
				},
				"required": []string{argument},
			},
		},
	}
}

func externalEvidenceUnavailableResult(
	requestID string,
	hypothesis unit.Hypothesis,
	capability tool.Tool,
	args map[string]any,
) (string, EvidenceReceipt) {
	argument := "query"
	if capability == WebFetch {
		argument = "url"
	}
	target := strings.TrimSpace(stringValue(args, argument))
	if target == "" {
		target = strings.TrimSpace(hypothesis.Trigger)
	}
	if target == "" {
		target = strings.TrimSpace(hypothesis.Content)
	}
	receipt := EvidenceReceipt{
		ToolCallID: requestID,
		Kind:       ExternalEvidenceUnverifiedReceipt,
		Ref:        hypothesis.ID,
	}
	result, _ := json.Marshal(map[string]any{
		"status":           "unavailable",
		"verification":     "unverified",
		"capability":       capability.Name(),
		"target":           target,
		"message":          "CCR cannot access this external source in the current Review 2 run.",
		"required_support": "insufficient",
		"receipt":          receipt,
	})
	return string(result), receipt
}
