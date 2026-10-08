package unit

import (
	"sort"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/language"
)

// FragmentID reuses repocli's identity; review context cannot change a target ID.
func FragmentID(f Fragment) string { return repocli.FragmentID(f.RepositoryFragment()) }

// NewRelatedUnit gives graph-grouped and file-coalesced Units collision-resistant
// identities. Fragments keep their exact diffs; scope only describes the view.
func NewRelatedUnit(fs []Fragment) Unit {
	fragments := append([]Fragment(nil), fs...)
	sort.Slice(fragments, func(i, j int) bool { return FragmentID(fragments[i]) < FragmentID(fragments[j]) })
	var ids []string
	for _, f := range fragments {
		ids = append(ids, FragmentID(f))
	}
	id := reviewID("unit", ids...)
	scope, formed := ScopeRelated, FormedGraph
	if len(fragments) == 1 {
		scope, formed = ScopeFile, FormedFile
		anchors := fragments[0].After
		if len(anchors) == 0 {
			anchors = fragments[0].Before
		}
		if len(anchors) > 0 {
			scope, formed = ScopeRelated, FormedGraph
		}
		if len(anchors) == 1 && (anchors[0].Kind == cg.Function || anchors[0].Kind == cg.Method) {
			scope, formed = ScopeFunc, FormedFunc
		}
	}
	return Unit{ID: id, Scope: scope, Formed: formed, Fragments: fragments, review: newReviewState()}
}

// TargetSummary is the formation trace; raw diff content remains on Fragment
// and in review messages, rather than being copied into every artifact.
type TargetSummary struct {
	ID          string            `json:"id"`
	Path        string            `json:"path"`
	OldPath     string            `json:"old_path,omitempty"`
	Before      []language.Anchor `json:"before,omitempty"`
	After       []language.Anchor `json:"after,omitempty"`
	Gaps        []string          `json:"gaps,omitempty"`
	BeforeEdits []language.Span   `json:"before_edits,omitempty"`
	AfterEdits  []language.Span   `json:"after_edits,omitempty"`
}

func (u Unit) Targets() []TargetSummary {
	out := make([]TargetSummary, 0, len(u.Fragments))
	for _, f := range u.Fragments {
		out = append(out, TargetSummary{ID: FragmentID(f), Path: f.Path, OldPath: f.OldPath, Before: f.Before, After: f.After, Gaps: f.Gaps, BeforeEdits: compactSpans(f.ChangedSpans(true)), AfterEdits: compactSpans(f.ChangedSpans(false))})
	}
	return out
}

// Persist edited ranges, not enclosing declaration spans: reviewing one method
// must not claim coverage of every edit in the surrounding class or file.
func compactSpans(spans []language.Span) []language.Span {
	var out []language.Span
	for _, span := range spans {
		if len(out) > 0 && span.Start <= out[len(out)-1].End+1 {
			if span.End > out[len(out)-1].End {
				out[len(out)-1].End = span.End
			}
		} else {
			out = append(out, span)
		}
	}
	return out
}
