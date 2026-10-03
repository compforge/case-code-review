package runner

import (
	"github.com/qiankunli/case-code-review/internal/language"
)

// The full upstream report stays in memory. Unresolved references can number in
// the tens of thousands; session evidence needs counts and samples, not a copy
// of every diagnostic or graph document.
func codeGraphArtifact(index *language.RepositoryIndex) map[string]any {
	counts := map[string]int{}
	for _, diagnostic := range index.Report.Diagnostics {
		counts[diagnostic.Code]++
	}
	return map[string]any{
		"snapshot":           index.Report.Snapshot,
		"duration_ms":        index.Duration.Milliseconds(),
		"documents":          len(index.Report.Documents),
		"parsed_documents":   len(index.Sources),
		"nodes":              index.Report.Nodes,
		"relations":          index.Report.Relations,
		"available":          index.Graph != nil,
		"diagnostic_counts":  counts,
		"diagnostic_samples": index.Report.Diagnostics[:min(32, len(index.Report.Diagnostics))],
		"gap_count":          len(index.Gaps),
		"gap_samples":        index.Gaps[:min(16, len(index.Gaps))],
	}
}

// CodeGraphReports exposes the same bounded analysis evidence as Session for
// no-LLM replay. A partial publication must not look like an empty dependency graph.
func (a *Runner) CodeGraphReports() map[string]any {
	reports := map[string]any{"after": codeGraphArtifact(a.sourceAnalyzer().Repository())}
	if a.beforeAnalyzer != nil {
		reports["before"] = codeGraphArtifact(a.beforeAnalyzer.Repository())
	}
	return reports
}
