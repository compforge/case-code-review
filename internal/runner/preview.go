package runner

import (
	"context"
	"fmt"

	previewmodel "github.com/qiankunli/case-code-review/internal/runner/preview"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type ExcludeReason = previewmodel.ExcludeReason
type Preview = previewmodel.Preview

const (
	ExcludeNone          = previewmodel.ExcludeNone
	ExcludeUserRule      = previewmodel.ExcludeUserRule
	ExcludeExtension     = previewmodel.ExcludeExtension
	ExcludeDefaultPath   = previewmodel.ExcludeDefaultPath
	ExcludeDeleted       = previewmodel.ExcludeDeleted
	ExcludeBinary        = previewmodel.ExcludeBinary
	ExcludeNonReviewRole = previewmodel.ExcludeNonReviewRole
)

// whyExcluded applies the filter algorithm as shouldReview but
// returns the specific reason a file is excluded.
func (a *Runner) whyExcluded(d change.Change) ExcludeReason {
	return previewmodel.Select(effectivePath(d), d.Tags, d.IsBinary, a.args.FileFilter)
}

// Preview loads diffs and applies the filter algorithm, returning structured
// preview data without dispatching any LLM calls.
func (a *Runner) Preview(ctx context.Context) (*Preview, error) {
	if err := a.loadChanges(ctx); err != nil {
		return nil, fmt.Errorf("load diffs: %w", err)
	}
	a.prepareFileSelections(ctx)
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

		selection, classified := a.selectionFor(d)
		if classified {
			entry.FileRole = selection.Roles.String()
			entry.ProvidesContext = selection.Context
			entry.WillReview = selection.Target
			entry.ExcludeReason = selection.Reason
			if selection.HasComponent {
				entry.ComponentRoot = selection.Component.Root
				entry.ComponentKind = string(selection.Component.Kind)
			}
		} else {
			reason := a.whyExcluded(d)
			entry.WillReview = reason == ExcludeNone
			entry.ExcludeReason = reason
		}

		switch {
		case entry.WillReview:
			result.ReviewableCount++
		case entry.ProvidesContext:
			result.ContextCount++
		default:
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
