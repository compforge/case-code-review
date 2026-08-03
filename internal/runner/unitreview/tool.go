package unitreview

import (
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

var SubmitHypothesis = tool.Named("submit_hypothesis")

const (
	HypothesisSubmitted       = "Hypothesis accepted for independent review. Do not resubmit it. Continue with the next material lead, or finish naturally when none remains."
	InvestigationWrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop investigating now. " +
		"Submit the one mature issue claim you have already finished if it has not been successfully accepted by submit_hypothesis, " +
		"including its trigger, impact, change attribution, evidence, and uncertainty. " +
		"Do not resubmit accepted Hypotheses, chase unresolved leads, or call other tools. " +
		"If no mature unsubmitted claim remains, finish now without a tool call."
)

func HypothesisToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name: SubmitHypothesis.Name(),
			Description: "Submit one mature, falsifiable issue hypothesis as soon as its shortest evidence chain is complete. " +
				"This tool may be called multiple times and does not end normal investigation; after a successful call, continue with the next material lead or finish naturally. " +
				"A separate reviewer will verify each hypothesis before any comment is published. Report plausible, " +
				"diff-caused defects with a concrete trigger and impact; state uncertainty instead of hiding it. " +
				"Do not wait to accumulate a batch and do not submit temporary leads.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repo-relative changed file containing the proposed comment anchor.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "Draft developer-facing explanation if the hypothesis survives Review and Trial.",
					},
					"existing_code": map[string]any{
						"type":        "string",
						"description": "Exact newly added code lines that anchor the hypothesis in the diff.",
					},
					"suggestion_code": map[string]any{
						"type":        "string",
						"description": "Optional replacement snippet.",
					},
					"trigger": map[string]any{
						"type":        "string",
						"description": "Concrete input, state, or execution path that activates the suspected defect.",
					},
					"impact": map[string]any{
						"type":        "string",
						"description": "Observable incorrect behavior if the trigger occurs.",
					},
					"change_attribution": map[string]any{
						"type":        "string",
						"description": "How this diff introduces or changes the behavior rather than merely exposing pre-existing code.",
					},
					"evidence": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Diff lines, source locations, contracts, or tool observations supporting the suspicion.",
					},
					"uncertainty": map[string]any{
						"type":        "string",
						"description": "The specific fact still needing independent verification; identify provider/SDK/API-owned behavior explicitly.",
					},
					"category": map[string]any{
						"type": "string",
						"enum": []string{"bug", "security", "performance", "maintainability", "test", "style", "documentation", "other"},
					},
					"severity": map[string]any{
						"type": "string",
						"enum": []string{"critical", "high", "medium", "low"},
					},
				},
				"required": []string{
					"path", "content", "existing_code", "trigger", "impact", "change_attribution",
					"evidence", "uncertainty", "category", "severity",
				},
			},
		},
	}
}

// InvestigationToolDefs gives the first review stage a hypothesis result tool
// while preserving its configured read-only investigation tools.
func InvestigationToolDefs(main []llm.ToolDef) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(main))
	replaced := false
	for _, def := range main {
		if def.Function.Name == tool.TaskDone.Name() {
			continue
		}
		if def.Function.Name == "code_comment" || def.Function.Name == SubmitHypothesis.Name() {
			if !replaced {
				out = append(out, HypothesisToolDef())
				replaced = true
			}
			continue
		}
		if def.Function.Name == tool.PostBulletin.Name() {
			def.Function.Description = strings.ReplaceAll(
				def.Function.Description, "code_comment", SubmitHypothesis.Name(),
			)
		}
		out = append(out, def)
	}
	if !replaced {
		out = append(out, HypothesisToolDef())
	}
	return out
}
