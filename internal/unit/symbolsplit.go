package unit

import (
	"context"
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"strings"
)

// BindFragments adds review identities to repository-owned fragments. It never
// reparses a diff or changes ownership/grouping; bindings serve Clue queries only.
func BindFragments(ctx context.Context, fs []repocli.Fragment, changes []change.Change, repoDir string, after, before *language.Analyzer) ([]Fragment, error) {
	if after == nil {
		after = language.NewAnalyzer(repoDir)
	}
	if before == nil {
		before = language.NewSnapshotAnalyzer(repoDir, "", nil)
	}
	type bindings struct {
		old, next []language.Anchor
		gaps      []string
	}
	type sourceKey struct {
		path    string
		deleted bool
	}
	byFile := map[sourceKey]bindings{}
	for _, d := range changes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var b bindings
		var err error
		if !d.IsNew && d.OldContentKnown {
			b.old, err = before.Anchors(ctx, language.Source{Path: d.OldPath, Content: d.OldFileContent})
			if err != nil {
				b.gaps = append(b.gaps, "before graph binding: "+err.Error())
			}
		}
		if !d.IsDeleted && !d.NewContentMissing {
			b.next, err = after.Anchors(ctx, language.Source{Path: d.NewPath, Content: d.NewFileContent})
			if err != nil {
				b.gaps = append(b.gaps, "after graph binding: "+err.Error())
			}
		}
		byFile[sourceKey{d.Path(), d.IsDeleted}] = b
	}
	// A file may contain separate deletion/addition records for a type change.
	// Select bindings by the captured source side, preserving each native fragment.
	var out []Fragment
	for _, source := range fs {
		b := byFile[sourceKey{source.Path, source.Status == "deleted"}]
		f := Fragment{Source: &source, Path: source.Path, OldPath: source.OldPath, Gaps: append(append([]string(nil), source.Gaps...), b.gaps...), Diff: source.Diff, Status: source.Status, Insertions: source.Insertions, Deletions: source.Deletions}
		f.Before = bindElements(source.Before, b.old)
		f.After = bindElements(source.After, b.next)
		for _, a := range f.After {
			if a.SymbolID != "" {
				f.Symbols = append(f.Symbols, a.SymbolID)
			}
		}
		out = append(out, f)
	}
	return out, nil
}

func bindElements(elements []repocli.SourceElement, anchors []language.Anchor) []language.Anchor {
	var out []language.Anchor
	seen := map[string]bool{}
	for _, e := range elements {
		for _, a := range anchors {
			if a.StartByte < e.StartByte || a.EndByte > e.EndByte {
				continue
			}
			if e.Name != "" && a.SymbolID != language.SymbolID(e.Path, "", e.Name) {
				continue
			}
			if !seen[a.NodeID] {
				seen[a.NodeID] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// RepositoryFragment projects manually supplied targets as well as native splits.
func (f Fragment) RepositoryFragment() repocli.Fragment {
	if f.Source != nil {
		return *f.Source
	}
	convert := func(as []language.Anchor) []repocli.SourceElement {
		var out []repocli.SourceElement
		for _, a := range as {
			name, _ := language.SymbolName(a.SymbolID)
			out = append(out, repocli.SourceElement{Path: a.Path, Name: name, Kind: repocli.ElementKind(strings.ToLower(string(a.Kind))), Span: repocli.SourceSpan{Start: a.Span.Start, End: a.Span.End}, StartByte: a.StartByte, EndByte: a.EndByte})
		}
		return out
	}
	return repocli.Fragment{Path: f.Path, OldPath: f.OldPath, Before: convert(f.Before), After: convert(f.After), Gaps: f.Gaps, Diff: f.Diff, Status: f.Status, Insertions: f.Insertions, Deletions: f.Deletions}
}
