package unit

import (
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// SnapshotFinder prevents old-side evidence from acquiring current symbol
// bindings. The delegate sees only the old owners and old coordinates.
type SnapshotFinder struct {
	Finder   ClueFinder
	Snapshot string
}

func (f SnapshotFinder) Find(u Unit) []Clue {
	old := u
	old.Fragments = nil
	for _, fragment := range u.Fragments {
		if len(fragment.Before) == 0 && fragment.Deletions == 0 {
			continue
		}
		fragment.BeforeView = true
		fragment.Path = fragment.OldPath
		if fragment.Path == "" {
			continue
		}
		fragment.Symbols = nil
		for _, a := range fragment.Before {
			if a.SymbolID != "" {
				fragment.Symbols = append(fragment.Symbols, a.SymbolID)
			}
		}
		old.Fragments = append(old.Fragments, fragment)
	}
	if len(old.Fragments) == 0 {
		return nil
	}
	clues := f.Finder.Find(old)
	for i := range clues {
		clues[i].Snapshot = f.Snapshot
	}
	return clues
}

func (f Fragment) ChangedSpans(before bool) []language.Span {
	var spans []language.Span
	for _, h := range change.ParseHunks(f.Diff) {
		old, new := h.OldStart, h.NewStart
		for _, l := range h.Lines {
			if before && l.Type == change.HunkDeleted {
				spans = append(spans, language.Span{Start: old, End: old})
			}
			if !before && l.Type == change.HunkAdded {
				spans = append(spans, language.Span{Start: new, End: new})
			}
			if l.Type != change.HunkAdded {
				old++
			}
			if l.Type != change.HunkDeleted {
				new++
			}
		}
	}
	return spans
}

// GraphNodes selects the captured side's native identities. Name-based inputs
// exist only for explicitly constructed Units without graph anchors; an invalid
// captured anchor never falls back to a same-named declaration.
func (u Unit) GraphNodes(index *language.RepositoryIndex) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, f := range u.Fragments {
		anchors := f.After
		if f.BeforeView {
			anchors = f.Before
		}
		if len(anchors) > 0 {
			for _, a := range anchors {
				if n, ok := index.AnchorNode(a); ok {
					add(n.ID)
				}
			}
		} else {
			for _, symbol := range f.Symbols {
				if n, ok := index.Declaration(symbol); ok {
					add(n.ID)
				}
			}
		}
	}
	return out
}
