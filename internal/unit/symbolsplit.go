package unit

import (
	"context"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// AutoSplitter attributes changed hunks to callable definitions reported by
// language.Analyzer. Unsupported or unparseable files degrade to file scope.
// It deliberately knows nothing about individual parser backends.
type AutoSplitter struct {
	RepoDir  string
	Analyzer *language.Analyzer
}

func (s AutoSplitter) Split(d change.Change) ([]Fragment, error) {
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
func splitByFuncSpans(d change.Change, spans []funcSpan) []Fragment {
	header := diffHeader(d.Diff)
	hunks := change.ParseHunks(d.Diff)
	grouped := make(map[int][]change.Hunk)
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

func funcOfHunk(h change.Hunk, spans []funcSpan) int {
	line := changedLine(h)
	for i := range spans {
		if line >= spans[i].start && line <= spans[i].end {
			return i
		}
	}
	return -1
}

func changedLine(h change.Hunk) int {
	line := h.NewStart
	for _, diffLine := range h.Lines {
		switch diffLine.Type {
		case change.HunkAdded:
			return line
		case change.HunkDeleted:
		default:
			line++
		}
	}
	return h.NewStart
}

func countChanges(hunks []change.Hunk) (insertions, deletions int64) {
	for _, hunk := range hunks {
		for _, line := range hunk.Lines {
			switch line.Type {
			case change.HunkAdded:
				insertions++
			case change.HunkDeleted:
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

func renderHunks(hunks []change.Hunk) string {
	var rendered strings.Builder
	for _, hunk := range hunks {
		fmt.Fprintf(&rendered, "@@ -%d,%d +%d,%d @@\n", hunk.OldStart, hunk.OldCount, hunk.NewStart, hunk.NewCount)
		for _, line := range hunk.Lines {
			switch line.Type {
			case change.HunkAdded:
				rendered.WriteString("+" + line.Content + "\n")
			case change.HunkDeleted:
				rendered.WriteString("-" + line.Content + "\n")
			default:
				rendered.WriteString(" " + line.Content + "\n")
			}
		}
	}
	return rendered.String()
}
