package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/qiankunli/case-code-review/internal/finding"
	"github.com/qiankunli/case-code-review/internal/runner"
	"github.com/qiankunli/case-code-review/internal/suggestdiff"
)

func outputText(comments []finding.Finding) {
	if len(comments) == 0 {
		fmt.Println("No comments generated. Looks good to me.")
		return
	}
	for _, c := range comments {
		renderComment(c)
	}
}

func hasSubtaskErrors(warnings []runner.Warning) bool {
	for _, w := range warnings {
		if w.Type == "subtask_error" {
			return true
		}
	}
	return false
}

func outputTextWithWarnings(comments []finding.Finding, warnings []runner.Warning) {
	if len(comments) == 0 {
		if hasSubtaskErrors(warnings) {
			fmt.Println("Some files could not be reviewed due to errors (see warnings below).")
		} else {
			fmt.Println("No comments generated. Looks good to me.")
		}
	} else {
		for _, c := range comments {
			renderComment(c)
		}
	}
	for _, w := range warnings {
		if w.Type == "subtask_error" {
			continue
		}
		fmt.Fprintf(os.Stderr, "[ccr] WARNING [%s] %s: %s\n", w.Type, sanitizeTerminal(w.File), sanitizeTerminal(w.Message))
	}
}

func renderComment(comment finding.Finding) {
	lines := buildDiffLines(comment)
	if len(lines) == 0 && comment.Content == "" {
		return
	}

	fmt.Printf("\n\033[2m─── %s:%d-%d ───\033[0m\n", sanitizeTerminal(comment.Path), comment.StartLine, comment.EndLine)

	if comment.Content != "" {
		content := sanitizeTerminal(comment.Content)
		// [category · severity] 徽章拼在正文前一起折行；两字段都缺时输出不变
		if badge := findingBadge(comment); badge != "" {
			content = badge + " " + content
		}
		for _, ln := range wrapByRunes(content, 100) {
			fmt.Printf("%s\n", ln)
		}
		fmt.Println()
	}

	if len(lines) > 0 {
		for _, dl := range lines {
			switch dl.Type {
			case suggestdiff.DiffAdded:
				printDiffLine("+", sanitizeTerminal(dl.Content), "\033[92m", "\033[48;2;0;60;0m")
			case suggestdiff.DiffDeleted:
				printDiffLine("-", sanitizeTerminal(dl.Content), "\033[91m", "\033[48;2;70;0;0m")
			case suggestdiff.DiffContext:
				printDiffLine(" ", sanitizeTerminal(dl.Content), "\033[2m", "\033[48;2;38;38;38m")
			}
		}
	}

	fmt.Println()
}

// printDiffLine renders a single diff line with colored prefix and background on content.
func printDiffLine(prefix, content, fgColor, bgColor string) {
	fmt.Printf("%s%s%s %s%s\033[0m\n", fgColor+bgColor, prefix, "\033[0m"+bgColor, content, "\033[0m")
}

// wrapByRunes splits text into lines that fit within maxWidth **rune** columns.
// Respects existing newlines and wraps at word boundaries.
func wrapByRunes(text string, maxW int) []string {
	if text == "" {
		return nil
	}
	var result []string
	for _, para := range strings.Split(text, "\n") {
		result = append(result, wrapSingleRuneLine(para, maxW)...)
	}
	return result
}

// wrapSingleRuneLine breaks one paragraph (no newlines) into rune-width-constrained lines.
func wrapSingleRuneLine(line string, maxW int) []string {
	runes := []rune(line)
	if visibleRunesLen(runes) <= maxW {
		return []string{line}
	}
	var result []string
	for len(runes) > 0 {
		cut := runeWrapCut(runes, maxW)
		result = append(result, string(runes[:cut]))
		runes = runes[cut:]
		// trim leading spaces of next segment
		for len(runes) > 0 && runes[0] == ' ' {
			runes = runes[1:]
		}
	}
	return result
}

// runeWrapCut returns a rune index suitable for breaking the line at ~maxW display width.
func runeWrapCut(runes []rune, maxW int) int {
	if visibleRunesLen(runes) <= maxW {
		return len(runes)
	}
	best := maxW
	if best >= len(runes) {
		return len(runes)
	}
	for i := best; i > 0; i-- {
		if runes[i] == ' ' || runes[i] == '\t' {
			return i
		}
	}
	return best
}

func visibleRunesLen(runes []rune) int {
	n := 0
	for _, r := range runes {
		if r >= 32 && r != 127 {
			n++
		}
	}
	return n
}

func sanitizeTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\t' || r == '\n' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func splitToLines(s string) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func buildDiffLines(comment finding.Finding) []suggestdiff.DiffLine {
	if comment.SuggestionCode == "" || comment.ExistingCode == "" {
		return nil
	}
	oldLines := splitToLines(comment.ExistingCode)
	newLines := splitToLines(comment.SuggestionCode)
	return suggestdiff.ComputeLineDiff(oldLines, newLines)
}

type jsonSummary struct {
	FilesReviewed    int64 `json:"files_reviewed"`
	Comments         int64 `json:"comments"`
	TotalTokens      int64 `json:"total_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int64 `json:"cache_write_tokens,omitempty"`
	// ElapsedSec is the review's wall-clock cost in whole seconds — numeric so
	// consumers (e.g. an MR-comment header) never parse a Go duration string.
	ElapsedSec int64 `json:"elapsed_sec"`
	// Models maps each routing alias that served a response to its count (deduped).
	// The review's model identity — present even on a clean (no-finding) review.
	// Omitted for a single-model (non-routing) run.
	Models map[string]int `json:"models,omitempty"`
}

type jsonToolCalls struct {
	Total  int64            `json:"total"`
	ByTool map[string]int64 `json:"by_tool"`
}

type jsonOutput struct {
	Status string `json:"status"`
	// Version is the reviewing ccr's own version (main.Version) — the review's
	// tool identity, present on every JSON shape including skipped/no-files.
	Version        string            `json:"version,omitempty"`
	Message        string            `json:"message,omitempty"`
	Summary        *jsonSummary      `json:"summary,omitempty"`
	ToolCalls      *jsonToolCalls    `json:"tool_calls"`
	Comments       []finding.Finding `json:"comments"`
	Warnings       []runner.Warning  `json:"warnings,omitempty"`
	ProjectSummary string            `json:"project_summary,omitempty"`
}

func outputJSON(comments []finding.Finding) error {
	out := jsonOutput{
		Status:   "success",
		Version:  Version,
		Comments: comments,
	}
	if len(comments) == 0 {
		out.Message = "No comments generated. Looks good to me."
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func outputJSONWithWarnings(comments []finding.Finding, warnings []runner.Warning,
	filesReviewed, inputTokens, outputTokens, totalTokens, cacheReadTokens, cacheWriteTokens int64,
	duration time.Duration, projectSummary string, toolCalls map[string]int64, models map[string]int) error {
	out := jsonOutput{
		Status:   "success",
		Version:  Version,
		Comments: comments,
		Summary: &jsonSummary{
			FilesReviewed:    filesReviewed,
			Comments:         int64(len(comments)),
			TotalTokens:      totalTokens,
			InputTokens:      inputTokens,
			OutputTokens:     outputTokens,
			CacheReadTokens:  cacheReadTokens,
			CacheWriteTokens: cacheWriteTokens,
			ElapsedSec:       int64(duration.Round(time.Second).Seconds()),
			Models:           models,
		},
		ProjectSummary: projectSummary,
	}
	var total int64
	for _, v := range toolCalls {
		total += v
	}
	byTool := toolCalls
	if byTool == nil {
		byTool = make(map[string]int64)
	}
	out.ToolCalls = &jsonToolCalls{
		Total:  total,
		ByTool: byTool,
	}
	if len(comments) == 0 {
		if hasSubtaskErrors(warnings) {
			out.Message = "Some files could not be reviewed due to errors."
		} else {
			out.Message = "No comments generated. Looks good to me."
		}
	}
	if len(warnings) > 0 {
		out.Warnings = warnings
		if hasSubtaskErrors(warnings) {
			out.Status = "completed_with_errors"
		} else {
			out.Status = "completed_with_warnings"
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// outputJSONFatal emits the failed-run JSON shape. In --format json the stdout
// contract is "always exactly one JSON object": a fatal run error must not
// leave stdout empty, or downstream parsers report their own parse failure
// instead of the actual error. Warnings ride along — on an all-units-failed
// run they carry the per-unit error reasons.
func outputJSONFatal(runErr error, warnings []runner.Warning) error {
	out := jsonOutput{
		Status:   "failed",
		Version:  Version,
		Message:  runErr.Error(),
		Comments: []finding.Finding{},
		ToolCalls: &jsonToolCalls{
			ByTool: map[string]int64{},
		},
	}
	if len(warnings) > 0 {
		out.Warnings = warnings
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func outputJSONNoFiles() error {
	out := jsonOutput{
		Status:   "skipped",
		Version:  Version,
		Message:  "No supported files changed.",
		Comments: []finding.Finding{},
		ToolCalls: &jsonToolCalls{
			ByTool: map[string]int64{},
		},
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// outputDryRunText prints each review unit's assembled context — what the LLM
// would receive — for `ccr review --dry-run`.
func outputDryRunText(units []runner.UnitContext) {
	if len(units) == 0 {
		fmt.Println("No reviewable units.")
		return
	}
	for _, u := range units {
		fmt.Printf("\n========== %s ==========\n", sanitizeTerminal(u.ID))
		dryRunSection("Governing Spec/Case", u.SpecCases)
		dryRunSection("Review Rules", u.Rules)
		dryRunSection("See Also", u.SeeAlso)
		dryRunSection("Prior Review", u.Prior)
	}
}

// dryRunMetrics is the structural summary of a dry-run — the numbers you compare
// across feature toggles (call-chain merge, clues) without spending LLM tokens.
type dryRunMetrics struct {
	UnitCount      int            `json:"unit_count"`
	ScopeCounts    map[string]int `json:"scope_counts"`     // func / file / callchain -> #units
	CrossFileUnits int            `json:"cross_file_units"` // units spanning >1 file (call-chain effect)
	FragmentsTotal int            `json:"fragments_total"`  // total changed regions across units
	ClueCoverage   map[string]int `json:"clue_coverage"`    // "<relation>/<kind>" -> #units carrying >=1 (the relation×kind matrix)
}

type dryRunJSON struct {
	Features map[string]bool      `json:"features"` // resolved feature gates — self-describes the run for A/B
	Preview  *runner.Preview      `json:"preview"`
	Metrics  dryRunMetrics        `json:"metrics"`
	Units    []runner.UnitContext `json:"units"`
	RepoMap  string               `json:"repo_map,omitempty"` // run-level ranked symbol map (shared by all units)
}

// outputDryRunJSON emits the dry-run as JSON: the resolved feature gates, the file
// preview, a structural metrics summary, and each unit's assembled context.
// Deterministic (no LLM), so it's the free layer for A/B-comparing what a feature
// changes — and it records which gates were active.
func outputDryRunJSON(preview *runner.Preview, units []runner.UnitContext, repoMap string, features map[string]bool) error {
	m := dryRunMetrics{
		UnitCount:    len(units),
		ScopeCounts:  map[string]int{},
		ClueCoverage: map[string]int{},
	}
	for _, u := range units {
		m.ScopeCounts[u.Scope]++
		if len(u.Paths) > 1 {
			m.CrossFileUnits++
		}
		m.FragmentsTotal += u.Fragments
		for cell, n := range u.Clues { // cell = "<relation>/<kind>"
			if n > 0 {
				m.ClueCoverage[cell]++
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(dryRunJSON{Features: features, Preview: preview, Metrics: m, Units: units, RepoMap: repoMap})
}

func dryRunSection(title, body string) {
	if strings.TrimSpace(body) == "" {
		body = "(none)"
	}
	fmt.Printf("── %s ──\n%s\n", title, body)
}

func outputPreviewText(p *runner.Preview) {
	if p.TotalFiles == 0 {
		fmt.Println("No files changed.")
		return
	}

	maxPathLen := 0
	for _, e := range p.Entries {
		if n := len(sanitizeTerminal(e.Path)); n > maxPathLen {
			maxPathLen = n
		}
	}
	if maxPathLen < 20 {
		maxPathLen = 20
	}
	pathFmt := fmt.Sprintf("%%-%ds", maxPathLen)

	fmt.Printf("\nPreview: %d file(s) changed  |  \033[32m+%d\033[0m  \033[31m-%d\033[0m\n",
		p.TotalFiles, p.TotalInsertions, p.TotalDeletions)

	if p.ReviewableCount > 0 {
		fmt.Printf("\n\033[1mWill review (%d):\033[0m\n", p.ReviewableCount)
		for _, e := range p.Entries {
			if !e.WillReview {
				continue
			}
			fmt.Printf("  %s  "+pathFmt+" \033[32m+%-4d\033[0m \033[31m-%-4d\033[0m\n",
				statusBadge(e.Status), sanitizeTerminal(e.Path), e.Insertions, e.Deletions)
		}
	}

	if p.ExcludedCount > 0 {
		fmt.Printf("\n\033[1mExcluded from review (%d):\033[0m\n", p.ExcludedCount)
		for _, e := range p.Entries {
			if e.WillReview {
				continue
			}
			fmt.Printf("  %s  "+pathFmt+" \033[2m(%s)\033[0m\n",
				statusBadge(e.Status), sanitizeTerminal(e.Path), sanitizeTerminal(string(e.ExcludeReason)))
		}
	}

	fmt.Println()
}

func statusBadge(status string) string {
	switch status {
	case "added":
		return "\033[32m[A]\033[0m"
	case "modified":
		return "\033[33m[M]\033[0m"
	case "deleted":
		return "\033[31m[D]\033[0m"
	case "renamed":
		return "\033[36m[R]\033[0m"
	case "binary":
		return "\033[35m[B]\033[0m"
	case "scan":
		return "\033[34m[S]\033[0m"
	default:
		return "[?]"
	}
}

// findingBadge renders the compact "[category · severity]" tag for a finding;
// empty when neither structured field is present so legacy output is unchanged.
func findingBadge(comment finding.Finding) string {
	c, s := sanitizeTerminal(comment.Category), sanitizeTerminal(comment.Severity)
	switch {
	case c != "" && s != "":
		return fmt.Sprintf("[%s · %s]", c, s)
	case c != "":
		return fmt.Sprintf("[%s]", c)
	case s != "":
		return fmt.Sprintf("[%s]", s)
	}
	return ""
}
