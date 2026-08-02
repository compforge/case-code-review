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
		for _, part := range splitHunkByFuncSpans(h, spans) {
			grouped[part.group] = append(grouped[part.group], part.hunk)
		}
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

type attributedHunk struct {
	group int
	hunk  change.Hunk
}

// splitHunkByFuncSpans handles the common case where git joins nearby changes
// from several functions into one @@ block. Attribution follows each line's
// post-change position, so a large unified hunk does not make its first changed
// function own every later function in the same block.
func splitHunkByFuncSpans(h change.Hunk, spans []funcSpan) []attributedHunk {
	oldLine, newLine := h.OldStart, h.NewStart
	group := -2
	part := change.Hunk{}
	changed := false
	var parts []attributedHunk

	flush := func() {
		if len(part.Lines) > 0 && changed {
			parts = append(parts, attributedHunk{group: group, hunk: part})
		}
		part = change.Hunk{}
		changed = false
	}

	for _, line := range h.Lines {
		owner := funcAtLine(newLine, spans)
		if owner != group {
			flush()
			group = owner
			part.OldStart = oldLine
			part.NewStart = newLine
		}
		part.Lines = append(part.Lines, line)
		switch line.Type {
		case change.HunkAdded:
			part.NewCount++
			newLine++
			changed = true
		case change.HunkDeleted:
			part.OldCount++
			oldLine++
			changed = true
		default:
			part.OldCount++
			part.NewCount++
			oldLine++
			newLine++
		}
	}
	flush()
	return parts
}

func funcAtLine(line int, spans []funcSpan) int {
	for i := range spans {
		if line >= spans[i].start && line <= spans[i].end {
			return i
		}
	}
	return -1
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
