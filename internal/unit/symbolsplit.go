package unit

import (
	"context"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/diff"
	"github.com/qiankunli/case-code-review/internal/language"
)

// AutoSplitter attributes changed hunks to callable definitions reported by
// language.Analyzer. Unsupported or unparseable files degrade to file scope.
// It deliberately knows nothing about individual parser backends.
type AutoSplitter struct {
	RepoDir  string
	Analyzer *language.Analyzer
}

func (s AutoSplitter) Split(d diff.Diff) ([]Fragment, error) {
	if d.NewFileContent == "" {
		return FileSplitter{}.Split(d)
	}
	if _, ok := language.Detect(d.NewPath); !ok {
		return FileSplitter{}.Split(d)
	}
	analyzer := s.Analyzer
	if analyzer == nil {
		analyzer = language.NewAnalyzer(s.RepoDir)
	}
	analysis, err := analyzer.Analyze(context.Background(), language.Source{
		Path: d.NewPath, Content: d.NewFileContent,
	})
	if err != nil {
		return FileSplitter{}.Split(d)
	}
	spans := make([]funcSpan, 0, len(analysis.Definitions))
	for _, definition := range analysis.Definitions {
		if definition.Callable() {
			spans = append(spans, funcSpan{
				start: definition.Span.Start,
				end:   definition.Span.End,
				id:    definition.SymbolID,
			})
		}
	}
	return splitByFuncSpans(d, spans), nil
}

// funcSpan is the minimal language fact needed by diff attribution.
type funcSpan struct {
	start, end int
	id         string
}

// splitByFuncSpans turns a file diff into one Fragment per touched function
// plus a residual file Fragment for changes outside every function.
func splitByFuncSpans(d diff.Diff, spans []funcSpan) []Fragment {
	header := diffHeader(d.Diff)
	hunks := diff.ParseHunks(d.Diff)
	grouped := make(map[int][]diff.Hunk)
	for _, h := range hunks {
		group := funcOfHunk(h, spans)
		grouped[group] = append(grouped[group], h)
	}

	var fragments []Fragment
	for i := range spans {
		hunks := grouped[i]
		if len(hunks) == 0 {
			continue
		}
		insertions, deletions := countChanges(hunks)
		fragments = append(fragments, Fragment{
			Path: d.NewPath, Symbols: []string{spans[i].id},
			Diff: header + renderHunks(hunks), Insertions: insertions, Deletions: deletions,
		})
	}
	if hunks := grouped[-1]; len(hunks) > 0 {
		insertions, deletions := countChanges(hunks)
		fragments = append(fragments, Fragment{
			Path: d.NewPath, Diff: header + renderHunks(hunks),
			Insertions: insertions, Deletions: deletions,
		})
	}
	if len(fragments) == 0 {
		fragments, _ = FileSplitter{}.Split(d)
	}
	return fragments
}

func funcOfHunk(h diff.Hunk, spans []funcSpan) int {
	line := changedLine(h)
	for i := range spans {
		if line >= spans[i].start && line <= spans[i].end {
			return i
		}
	}
	return -1
}

func changedLine(h diff.Hunk) int {
	line := h.NewStart
	for _, diffLine := range h.Lines {
		switch diffLine.Type {
		case diff.HunkAdded:
			return line
		case diff.HunkDeleted:
		default:
			line++
		}
	}
	return h.NewStart
}

func countChanges(hunks []diff.Hunk) (insertions, deletions int64) {
	for _, hunk := range hunks {
		for _, line := range hunk.Lines {
			switch line.Type {
			case diff.HunkAdded:
				insertions++
			case diff.HunkDeleted:
				deletions++
			}
		}
	}
	return insertions, deletions
}

func diffHeader(rawDiff string) string {
	if i := strings.Index(rawDiff, "\n@@"); i >= 0 {
		return rawDiff[:i+1]
	}
	if strings.HasPrefix(rawDiff, "@@") {
		return ""
	}
	return rawDiff
}

func renderHunks(hunks []diff.Hunk) string {
	var rendered strings.Builder
	for _, hunk := range hunks {
		fmt.Fprintf(&rendered, "@@ -%d,%d +%d,%d @@\n", hunk.OldStart, hunk.OldCount, hunk.NewStart, hunk.NewCount)
		for _, line := range hunk.Lines {
			switch line.Type {
			case diff.HunkAdded:
				rendered.WriteString("+" + line.Content + "\n")
			case diff.HunkDeleted:
				rendered.WriteString("-" + line.Content + "\n")
			default:
				rendered.WriteString(" " + line.Content + "\n")
			}
		}
	}
	return rendered.String()
}
