package unit

import (
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// BoundFragment keeps every changed line while splitting oversized targets at
// patch coordinates. A source line is indivisible; if it alone exceeds budget
// the target is retained with an explicit gap rather than silently filtered out.
func BoundFragment(f Fragment, tokens int, lines int64) []Fragment {
	if llm.CountTokens(f.Diff) <= tokens && f.Insertions+f.Deletions <= lines {
		return []Fragment{f}
	}
	header := diffHeader(f.Diff)
	var out []Fragment
	for _, h := range change.ParseHunks(f.Diff) {
		old, new := h.OldStart, h.NewStart
		part := change.Hunk{OldStart: old, NewStart: new}
		flush := func() {
			ins, del := countChanges([]change.Hunk{part})
			if ins+del > 0 {
				piece := f
				piece.Diff = header + renderHunks([]change.Hunk{part})
				piece.Insertions = ins
				piece.Deletions = del
				piece.Gaps = append([]string(nil), f.Gaps...)
				if llm.CountTokens(piece.Diff) > tokens {
					piece.Gaps = append(piece.Gaps, "indivisible patch line exceeds diff-token budget")
				}
				out = append(out, piece)
			}
			part = change.Hunk{OldStart: old, NewStart: new}
		}
		for _, line := range h.Lines {
			candidate := part
			candidate.Lines = append(append([]change.HunkLine(nil), part.Lines...), line)
			if line.Type != change.HunkAdded {
				candidate.OldCount++
			}
			if line.Type != change.HunkDeleted {
				candidate.NewCount++
			}
			ins, del := countChanges([]change.Hunk{candidate})
			if len(part.Lines) > 0 && (llm.CountTokens(header+renderHunks([]change.Hunk{candidate})) > tokens || ins+del > lines) {
				flush()
			}
			part.Lines = append(part.Lines, line)
			if line.Type != change.HunkAdded {
				part.OldCount++
				old++
			}
			if line.Type != change.HunkDeleted {
				part.NewCount++
				new++
			}
		}
		flush()
	}
	if len(out) == 0 {
		f.Gaps = append(append([]string(nil), f.Gaps...), "unsplittable diff exceeds budget")
		return []Fragment{f}
	}
	return out
}
