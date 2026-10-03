package unit

import (
	"sort"
	"strings"

	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// splitGraphChange partitions edits, not AST nodes. A replacement block can
// touch several owners; keeping that patch together does not assert that an old
// declaration and a new declaration have the same identity.
func splitGraphChange(d change.Change, before, after []language.Anchor, gaps []string) []Fragment {
	var out []Fragment
	grouped := map[string]int{}
	for _, h := range change.ParseHunks(d.Diff) {
		oldLine, newLine := h.OldStart, h.NewStart
		for i := 0; i < len(h.Lines); {
			if h.Lines[i].Type == change.HunkContext {
				oldLine++
				newLine++
				i++
				continue
			}
			start := i
			oldStart, newStart := oldLine, newLine
			var oldOwners, newOwners []language.Anchor
			// A contiguous replacement is one patch. Pure additions/removals may be
			// split at ownership boundaries without inventing cross-version matches.
			end := i
			added, deleted := false, false
			for end < len(h.Lines) && h.Lines[end].Type != change.HunkContext {
				added = added || h.Lines[end].Type == change.HunkAdded
				deleted = deleted || h.Lines[end].Type == change.HunkDeleted
				end++
			}
			var owner string
			for i < end {
				l := h.Lines[i]
				anchors, line := after, newLine
				if l.Type == change.HunkDeleted {
					anchors, line = before, oldLine
				}
				a, ok := language.AnchorAt(anchors, line)
				key := a.NodeID
				if i > start && !(added && deleted) && key != owner {
					break
				}
				owner = key
				if l.Type == change.HunkDeleted {
					if ok {
						oldOwners = appendAnchor(oldOwners, a)
					}
					oldLine++
				} else {
					if ok {
						newOwners = appendAnchor(newOwners, a)
					}
					newLine++
				}
				i++
			}
			part := change.Hunk{OldStart: oldStart, NewStart: newStart, OldCount: oldLine - oldStart, NewCount: newLine - newStart, Lines: append([]change.HunkLine(nil), h.Lines[start:i]...)}
			// Include only neighboring unchanged lines: changed lines retain one owner.
			for j := start - 1; j >= 0 && start-j <= 3 && h.Lines[j].Type == change.HunkContext; j-- {
				part.OldStart--
				part.NewStart--
				part.OldCount++
				part.NewCount++
				part.Lines = append([]change.HunkLine{h.Lines[j]}, part.Lines...)
			}
			for j := i; j < len(h.Lines) && j-i < 3 && h.Lines[j].Type == change.HunkContext; j++ {
				part.OldCount++
				part.NewCount++
				part.Lines = append(part.Lines, h.Lines[j])
			}
			sortAnchors(oldOwners)
			sortAnchors(newOwners)
			key := anchorKey(oldOwners) + "|" + anchorKey(newOwners)
			ins, del := countChanges([]change.Hunk{part})
			if index, ok := grouped[key]; ok && key != "|" {
				f := &out[index]
				f.Diff += renderHunks([]change.Hunk{part})
				f.Insertions += ins
				f.Deletions += del
			} else {
				f := Fragment{Path: d.Path(), OldPath: d.OldPath, Before: oldOwners, After: newOwners, Diff: diffHeader(d.Diff) + renderHunks([]change.Hunk{part}), Insertions: ins, Deletions: del, Gaps: gaps}
				if d.IsDeleted {
					f.Status = "deleted"
				} else if d.IsNew {
					f.Status = "added"
				} else if d.IsRenamed {
					f.Status = "renamed"
				}
				for _, a := range newOwners {
					if a.SymbolID != "" {
						f.Symbols = append(f.Symbols, a.SymbolID)
					}
				}
				grouped[key] = len(out)
				out = append(out, f)
			}
		}
	}
	if len(out) == 0 {
		out, _ = FileSplitter{}.Split(d)
		out[0].Gaps = gaps
	}
	return out
}

func appendAnchor(as []language.Anchor, a language.Anchor) []language.Anchor {
	for _, x := range as {
		if x.NodeID == a.NodeID {
			return as
		}
	}
	return append(as, a)
}
func sortAnchors(as []language.Anchor) {
	sort.Slice(as, func(i, j int) bool { return as[i].NodeID < as[j].NodeID })
}
func anchorKey(as []language.Anchor) string {
	var ids []string
	for _, a := range as {
		ids = append(ids, a.Snapshot+":"+a.NodeID)
	}
	return strings.Join(ids, "\x00")
}

// FragmentID is scoped to the exact patch and its source identities. Labels and
// short names must not join state belonging to different files or revisions.
func FragmentID(f Fragment) string {
	return reviewID("fragment", f.Path, f.OldPath, anchorKey(f.Before), anchorKey(f.After), f.Diff)
}

// NewRelatedUnit gives graph-grouped and budget-split Units collision-resistant
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
	ID      string            `json:"id"`
	Path    string            `json:"path"`
	OldPath string            `json:"old_path,omitempty"`
	Before  []language.Anchor `json:"before,omitempty"`
	After   []language.Anchor `json:"after,omitempty"`
	Gaps    []string          `json:"gaps,omitempty"`
}

func (u Unit) Targets() []TargetSummary {
	out := make([]TargetSummary, 0, len(u.Fragments))
	for _, f := range u.Fragments {
		out = append(out, TargetSummary{ID: FragmentID(f), Path: f.Path, OldPath: f.OldPath, Before: f.Before, After: f.After, Gaps: f.Gaps})
	}
	return out
}
