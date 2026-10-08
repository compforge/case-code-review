package unit

import (
	"context"
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"strings"
)

// AutoSplitter delegates repository splitting to repocli, then attaches CCR's
// graph identities for contract and source-context lookup.
type AutoSplitter struct {
	RepoDir  string
	Analyzer *language.Analyzer
	Before   *language.Analyzer
}

func (s AutoSplitter) Split(d change.Change) ([]Fragment, error) {
	fs, err := repocli.SplitChange(context.Background(), d)
	if err != nil {
		return nil, err
	}
	after := s.Analyzer
	if after == nil {
		after = language.NewAnalyzer(s.RepoDir)
	}
	before := s.Before
	if before == nil {
		before = language.NewSnapshotAnalyzer(s.RepoDir, d.BeforeRef, nil)
	}
	var old, next []language.Anchor
	var bindingGaps []string
	if !d.IsNew && d.OldContentKnown {
		old, err = before.Anchors(context.Background(), language.Source{Path: d.OldPath, Content: d.OldFileContent})
		if err != nil {
			bindingGaps = append(bindingGaps, "before graph binding: "+err.Error())
		}
	}
	if !d.IsDeleted && !d.NewContentMissing {
		next, err = after.Anchors(context.Background(), language.Source{Path: d.NewPath, Content: d.NewFileContent})
		if err != nil {
			bindingGaps = append(bindingGaps, "after graph binding: "+err.Error())
		}
	}
	var out []Fragment
	for _, source := range fs {
		f := Fragment{Source: &source, Path: source.Path, OldPath: source.OldPath, Gaps: source.Gaps, Diff: source.Diff, Status: source.Status, Insertions: source.Insertions, Deletions: source.Deletions}
		f.Gaps = append(append([]string(nil), f.Gaps...), bindingGaps...)
		f.Before = bindElements(source.Before, old)
		f.After = bindElements(source.After, next)
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
