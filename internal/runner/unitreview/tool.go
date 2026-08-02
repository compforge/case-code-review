package unitreview

import (
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

var SubmitHypotheses = tool.Named("submit_hypotheses")

const (
	HypothesesSubmitted       = "Unit review completed; hypotheses submitted for independent review."
	InvestigationWrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop investigating now. " +
		"Submit every concrete remaining issue claim with submit_hypotheses, including its trigger, impact, " +
		"change attribution, evidence, and uncertainty. Submit an empty array when no material claim remains. " +
		"Do not call other tools."
)

func HypothesisToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name: SubmitHypotheses.Name(),
			Description: "Complete this Unit Review by submitting every falsifiable issue hypothesis found during investigation. " +
				"A separate reviewer will verify them before any comment is published. Report plausible, " +
				"diff-caused defects with a concrete trigger and impact; state uncertainty instead of hiding it. " +
				"Use an empty hypotheses array when no material issue remains. A successful call ends the review.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"hypotheses": map[string]any{
						"type": "array",
						"items": map[string]any{
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
									"description": "The specific fact still needing independent verification; empty only when evidence is already complete.",
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
				},
				"required": []string{"hypotheses"},
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
		if def.Function.Name == "code_comment" || def.Function.Name == SubmitHypotheses.Name() {
			if !replaced {
				out = append(out, HypothesisToolDef())
				replaced = true
			}
			continue
		}
		if def.Function.Name == tool.PostBulletin.Name() {
			def.Function.Description = strings.ReplaceAll(
				def.Function.Description, "code_comment", SubmitHypotheses.Name(),
			)
		}
		out = append(out, def)
	}
	if !replaced {
		out = append(out, HypothesisToolDef())
	}
	return out
}
