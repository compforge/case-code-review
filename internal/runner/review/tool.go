package review

import (
	"strings"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

var ReportHypothesis = tool.Named("report_hypothesis")

const (
	HypothesisSubmitted       = "Hypothesis recorded for independent review."
	InvestigationWrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop investigating now. " +
		"Report every concrete remaining issue claim with report_hypothesis, including its trigger, impact, " +
		"change attribution, evidence, and uncertainty; then call task_done. Do not call other tools."
	HypothesisReviewWrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop gathering evidence now. " +
		"Submit one assessment for every supplied hypothesis using only the evidence already gathered, " +
		"then call task_done. Use insufficient/unknown when a decisive fact is still missing."
)

func HypothesisToolDef() llm.ToolDef {
	return llm.ToolDef{
		Type: "function",
		Function: llm.FunctionDef{
			Name: ReportHypothesis.Name(),
			Description: "Record one or more falsifiable issue hypotheses found during investigation. " +
				"A separate reviewer will verify them before any comment is published. Report plausible, " +
				"diff-caused defects with a concrete trigger and impact; state uncertainty instead of hiding it.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Repo-relative changed file containing the proposed comment anchor.",
					},
					"hypotheses": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
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
								"content", "existing_code", "trigger", "impact", "change_attribution",
								"evidence", "uncertainty", "category", "severity",
							},
						},
					},
				},
				"required": []string{"path", "hypotheses"},
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
		if def.Function.Name == "code_comment" || def.Function.Name == ReportHypothesis.Name() {
			out = append(out, HypothesisToolDef())
			replaced = true
			continue
		}
		if def.Function.Name == tool.PostBulletin.Name() {
			def.Function.Description = strings.ReplaceAll(
				def.Function.Description, "code_comment", ReportHypothesis.Name(),
			)
		}
		out = append(out, def)
	}
	if !replaced {
		out = append(out, HypothesisToolDef())
	}
	return out
}

// HypothesisReviewToolDefs is a structural allowlist: the convergent reviewer
// can inspect code and submit assessments, but cannot publish comments, propose
// new hypotheses, or write to the Review Team board.
func HypothesisReviewToolDefs(main []llm.ToolDef) []llm.ToolDef {
	allowed := map[string]bool{
		tool.TaskDone.Name():     true,
		tool.FileRead.Name():     true,
		tool.FileFind.Name():     true,
		tool.FileReadDiff.Name(): true,
		tool.CodeSearch.Name():   true,
	}
	out := make([]llm.ToolDef, 0, len(main)+1)
	for _, def := range main {
		if allowed[def.Function.Name] {
			out = append(out, def)
		}
	}
	out = append(out, AssessmentToolDef())
	return out
}
