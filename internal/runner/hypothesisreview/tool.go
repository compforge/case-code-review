package hypothesisreview

import (
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
)

const WrapUpPrompt = "BUDGET NEARLY EXHAUSTED — stop gathering evidence now. " +
	"Submit the current hypothesis assessment using only the evidence already gathered. " +
	"A valid submit_assessments call ends this review. Use insufficient/unknown when a decisive fact is still missing; do not " +
	"claim support without the required diff evidence receipt."

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
	out := make([]llm.ToolDef, 0, len(main)+1)
	for _, def := range main {
		if allowed[def.Function.Name] {
			out = append(out, def)
		}
	}
	out = append(out, AssessmentToolDef())
	return out
}
