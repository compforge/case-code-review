package unit

import (
	"context"
	"fmt"
	"strings"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// AutoSplitter locates diff edits in CodeGraph publications. A source item can
// be a declaration or binding; unsupported syntax remains an unbound fragment.
type AutoSplitter struct {
	RepoDir  string
	Analyzer *language.Analyzer
	Before   *language.Analyzer
}

func (s AutoSplitter) Split(d change.Change) ([]Fragment, error) {
	a := s.Analyzer
	if a == nil {
		a = language.NewAnalyzer(s.RepoDir)
	}
	var before, after []language.Anchor
	var gaps []string
	if d.NewContentMissing {
		gaps = append(gaps, "after: source unavailable")
	}
	if !d.IsDeleted && d.NewFileContent != "" {
		var err error
		after, err = a.Anchors(context.Background(), language.Source{Path: d.NewPath, Content: d.NewFileContent})
		if err != nil {
			gaps = append(gaps, "after: "+err.Error())
		}
	}
	if !d.IsNew && d.OldContentKnown && d.OldFileContent != "" {
		old := s.Before
		if old == nil {
			old = language.NewSnapshotAnalyzer(s.RepoDir, d.BeforeRef, nil)
		}
		var err error
		before, err = old.Anchors(context.Background(), language.Source{Path: d.OldPath, Content: d.OldFileContent})
		if err != nil {
			gaps = append(gaps, "before: "+err.Error())
		}
	} else if d.Deletions > 0 && !d.OldContentKnown {
		gaps = append(gaps, "before: source unavailable")
	}
	return splitGraphChange(d, before, after, gaps), nil
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
	if strings.HasPrefix(rawDiff, "@@") {
		return ""
	}
	if i := strings.Index(rawDiff, "\n@@"); i >= 0 {
		return rawDiff[:i+1]
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
