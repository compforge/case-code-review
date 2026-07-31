package runner

import (
	"context"
	"fmt"

	allowedext "github.com/qiankunli/case-code-review/internal/config/allowlist"
	previewmodel "github.com/qiankunli/case-code-review/internal/runner/preview"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type ExcludeReason = previewmodel.ExcludeReason
type Preview = previewmodel.Preview

const (
	ExcludeNone        = previewmodel.ExcludeNone
	ExcludeUserRule    = previewmodel.ExcludeUserRule
	ExcludeExtension   = previewmodel.ExcludeExtension
	ExcludeDefaultPath = previewmodel.ExcludeDefaultPath
	ExcludeDeleted     = previewmodel.ExcludeDeleted
	ExcludeBinary      = previewmodel.ExcludeBinary
)

// whyExcluded applies the filter algorithm as shouldReview but
// returns the specific reason a file is excluded.
func (a *Runner) whyExcluded(d change.Change) ExcludeReason {
	if d.IsBinary {
		return ExcludeBinary
	}

	path := effectivePath(d)
	f := a.args.FileFilter

	if f != nil && f.IsUserExcluded(path) {
		return ExcludeUserRule
	}

	if f != nil && f.HasInclude() && f.IsUserIncluded(path) {
		return ExcludeNone
	}

	ext := a.extFromPath(path)
	if ext != "" && !allowedext.IsAllowedExt(ext) {
		return ExcludeExtension
	}

	if allowedext.IsExcludedPath(path) {
		return ExcludeDefaultPath
	}

	return ExcludeNone
}

// Preview loads diffs and applies the filter algorithm, returning structured
// preview data without dispatching any LLM calls.
func (a *Runner) Preview(ctx context.Context) (*Preview, error) {
	if err := a.loadChanges(ctx); err != nil {
		return nil, fmt.Errorf("load diffs: %w", err)
	}
	return a.buildPreview(), nil
}

// buildPreview turns the already-loaded diffs into preview data (no I/O), so a
// caller that has loaded diffs once (e.g. DryRun) can reuse it without re-parsing
// — loadDiffs accumulates totals, so calling it twice would double-count.
func (a *Runner) buildPreview() *Preview {
	result := &Preview{
		TotalInsertions: a.totalInsertions,
		TotalDeletions:  a.totalDeletions,
		TotalFiles:      len(a.changes),
	}

	for _, d := range a.changes {
		path := effectivePath(d)
		entry := previewmodel.Entry{
			Path:       path,
			Insertions: d.Insertions,
			Deletions:  d.Deletions,
			Status:     diffStatus(d),
		}

		reason := a.whyExcluded(d)
		if reason == ExcludeNone && d.IsDeleted {
			reason = ExcludeDeleted
		}

		entry.WillReview = reason == ExcludeNone
		entry.ExcludeReason = reason

		if entry.WillReview {
			result.ReviewableCount++
		} else {
			result.ExcludedCount++
		}

		result.Entries = append(result.Entries, entry)
	}

	return result
}

func effectivePath(d change.Change) string {
	if d.NewPath == "/dev/null" {
		return d.OldPath
	}
	return d.NewPath
}

func diffStatus(d change.Change) string {
	switch {
	case d.IsBinary:
		return "binary"
	case d.IsNew:
		return "added"
	case d.IsDeleted:
		return "deleted"
	case d.IsRenamed:
		return "renamed"
	case d.OldPath != d.NewPath && d.OldPath != "" && d.OldPath != "/dev/null":
		return "renamed"
	default:
		return "modified"
	}
}
