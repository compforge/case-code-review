package spec

import (
	"sort"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// This file is the factored context pipeline of docs/unit-model.md: the
// relation axis (RelationCollector: unit → related symbols) × the source axis
// (cluesFor: symbol → authored marks + derived docstring). RelatedFinder composes
// the two into one unit.ClueFinder, so adding a relation or a source never
// multiplies finder types.

// RelatedSymbol is one symbol reached from a review unit along a typed relation —
// what the relation axis hands to the source axis.
type RelatedSymbol struct {
	ID       string // local symbol-id ("" when the symbol isn't in this repo's index)
	Relation unit.Relation
	Name     string // bare name as referenced (labels authored marks)
	Ref      string // Clue.Ref for the doc clue (local symbol-id or external FQN)
	DocFile  string // source file for docstring extraction ("" = no doc)
	DocName  string // symbol name inside DocFile
	Doc      string // rendered documentation from the same graph publication
	// Entry is the resolved spec entry when the collector already knows it —
	// required for dependency symbols, which have no local symbol-id (they
	// resolve by fqn). Nil means "look ID up in the local index".
	Entry *Entry
}

// RelationCollector finds the symbols related to a unit along one relation.
type RelationCollector interface {
	Related(u unit.Unit) []RelatedSymbol
}

// --- self: the changed symbols themselves ---

type selfCollector struct{ analyzer *language.Analyzer }

func (c selfCollector) Related(u unit.Unit) []RelatedSymbol {
	var out []RelatedSymbol
	for _, sym := range u.AllSymbols() {
		name := sym
		if parsed, ok := language.SymbolName(sym); ok {
			name = parsed
		}
		out = append(out, RelatedSymbol{ID: sym, Relation: unit.RelSelf, Name: name, Ref: sym, Doc: c.analyzer.RepositoryDoc(sym)})
	}
	return out
}

// --- owner: a changed method's enclosing type (or nested func's outer func) ---

// Without the owner relation, a class-level marker only fires when the whole
// class is the changed symbol — which almost never happens; changing `Svc.create`
// must still surface class `Svc`'s @rule and docstring.
type ownerCollector struct{ analyzer *language.Analyzer }

func (c ownerCollector) Related(u unit.Unit) []RelatedSymbol {
	own := map[string]bool{}
	for _, id := range u.AllSymbols() {
		own[id] = true
	}
	seen := map[string]bool{}
	var out []RelatedSymbol
	for _, id := range u.AllSymbols() {
		for _, owner := range c.analyzer.Repository().Owners(id) {
			if own[owner] || seen[owner] {
				continue
			}
			seen[owner] = true
			name, _ := language.SymbolName(owner)
			out = append(out, RelatedSymbol{ID: owner, Relation: unit.RelOwner, Name: name, Ref: owner, Doc: c.analyzer.RepositoryDoc(owner)})
		}
	}
	return out
}

// Used contracts follow published source uses and aliases. The contract catalog
// supplies authored meaning; it cannot prove a source binding by matching a name.
type usedCollector struct {
	byFqn    map[string]fqnHit
	analyzer *language.Analyzer
}
type fqnHit struct {
	id    string
	entry Entry
}

func newUsedCollector(cat Catalog, analyzer *language.Analyzer) usedCollector {
	byFqn := map[string]fqnHit{}
	for fqn, e := range cat.Deps {
		byFqn[fqn] = fqnHit{entry: e}
	}
	ids := make([]string, 0, len(cat.Local))
	for id := range cat.Local {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if e := cat.Local[id]; e.Fqn != "" {
			byFqn[e.Fqn] = fqnHit{id: id, entry: e}
		}
	}
	return usedCollector{byFqn: byFqn, analyzer: analyzer}
}
func (c usedCollector) Related(u unit.Unit) []RelatedSymbol {
	own := map[string]bool{}
	for _, id := range u.AllSymbols() {
		own[id] = true
	}
	seen := map[string]bool{}
	var out []RelatedSymbol
	for _, f := range u.Fragments {
		spans := changedSourceSpans(f, c.analyzer.Repository())
		for _, ref := range c.analyzer.ReferencesAt(f.Path, spans) {
			rs := RelatedSymbol{ID: ref.SymbolID, Relation: unit.RelUsed, Name: ref.Name, Ref: ref.SymbolID}
			if rs.ID != "" {
				rs.Doc = c.analyzer.RepositoryDoc(rs.ID)
			} else {
				rs.Ref = ref.FQN
				rs.DocFile, rs.DocName = ref.SourcePath, ref.SourceName
				if hit, ok := c.byFqn[ref.FQN]; ok {
					rs.ID, rs.Entry = hit.id, &hit.entry
				}
			}
			key := rs.ID + "\x00" + rs.Ref
			if own[rs.ID] || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, rs)
		}
	}
	return out
}

// Coordinates select code facts; deleted lines cannot be rebound in the target
// snapshot. Headerless synthetic fragments can select an already-known symbol.
func changedSourceSpans(f unit.Fragment, index *language.RepositoryIndex) []language.Span {
	hunks := change.ParseHunks(f.Diff)
	spans := f.ChangedSpans(f.BeforeView)
	if len(hunks) == 0 {
		for _, id := range f.Symbols {
			if n, ok := index.Declaration(id); ok && n.Location.Path == f.Path {
				spans = append(spans, language.Span{Start: n.Location.Line, End: n.Location.EndLine})
			}
		}
	}
	return spans
}

// --- the composed finder: relation axis × source axis ---

// KindGates mirrors the spec_case/rule/link/doc feature gates. A gate switches
// its clue KIND off across every relation (self/owner/used alike), so an
// ablation run measures "ccr without that evidence kind" — the gate axis and the
// dry-run relation×kind matrix share one coordinate system. Relations themselves
// are not gated: they're the cheap mechanism, kinds are the evidence.
type KindGates struct{ Spec, Rule, Link, Doc bool }

// RelatedFinder is the unit.ClueFinder over the self/owner/used relations.
type RelatedFinder struct {
	local      Index // this repo's entries; dependency entries reach cluesFor via RelatedSymbol.Entry
	gates      KindGates
	collectors []RelationCollector
}

func NewRelatedFinder(cat Catalog, analyzer *language.Analyzer, gates KindGates) RelatedFinder {
	return RelatedFinder{
		local: cat.Local,
		gates: gates,
		collectors: []RelationCollector{
			selfCollector{analyzer: analyzer},
			ownerCollector{analyzer: analyzer},
			newUsedCollector(cat, analyzer),
		},
	}
}

func (f RelatedFinder) Find(u unit.Unit) []unit.Clue {
	if !f.gates.Spec && !f.gates.Rule && !f.gates.Link && !f.gates.Doc {
		return nil
	}
	var clues []unit.Clue
	for _, c := range f.collectors {
		for _, rs := range c.Related(u) {
			clues = append(clues, f.cluesFor(rs)...)
		}
	}
	return clues
}

// cluesFor is the source axis: a related symbol's authored marks (its resolved
// entry, or a local-index lookup) and derived documentation. Text is
// RAW content and Ref the source identity — how a clue reached the unit is
// worded at render time from (relation, kind, ref), not here.
func (f RelatedFinder) cluesFor(rs RelatedSymbol) []unit.Clue {
	var clues []unit.Clue
	e := rs.Entry
	if e == nil {
		local := f.local[rs.ID]
		e = &local
	}
	switch rs.Relation {
	case unit.RelSelf:
		if f.gates.Spec {
			if r := f.local.Render([]string{rs.ID}); r != "" {
				clues = append(clues, unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelSelf, Text: r, Ref: rs.ID})
			}
		}
		if f.gates.Rule {
			for _, r := range e.Rules {
				clues = append(clues, unit.Clue{Kind: unit.ClueRule, Relation: unit.RelSelf, Text: r, Ref: rs.ID})
			}
		}
		if f.gates.Link {
			clues = append(clues, linkClues(e.Links, unit.RelSelf)...)
		}
	case unit.RelOwner:
		if f.gates.Spec {
			if r := f.local.Render([]string{rs.ID}); r != "" {
				clues = append(clues, unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelOwner, Text: r, Ref: rs.ID})
			}
		}
		if f.gates.Rule {
			for _, r := range e.Rules {
				clues = append(clues, unit.Clue{Kind: unit.ClueRule, Relation: unit.RelOwner, Text: r, Ref: rs.ID})
			}
		}
		if f.gates.Link {
			clues = append(clues, linkClues(e.Links, unit.RelOwner)...)
		}
	case unit.RelUsed:
		// used injects the referenced symbol's contract (spec) and usage rules —
		// both are constraints on this change. Its cases/links stay out: another
		// symbol's scenario checklist and see-alsos are noise here.
		if f.gates.Spec && e.Spec != "" {
			clues = append(clues, unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelUsed, Text: e.Spec, Ref: rs.Name})
		}
		if f.gates.Rule {
			for _, r := range e.Rules {
				clues = append(clues, unit.Clue{Kind: unit.ClueRule, Relation: unit.RelUsed, Text: r, Ref: rs.Name})
			}
		}
	}
	if f.gates.Doc {
		doc := rs.Doc
		if doc == "" && rs.DocFile != "" {
			doc = extractDocFromFile(rs.DocFile, rs.DocName)
		}
		if doc != "" {
			clues = append(clues, unit.Clue{Kind: unit.ClueDoc, Relation: rs.Relation, Text: doc, Ref: rs.Ref})
		}
	}
	return clues
}

// linkClues labels @link pointers doc/function for the prompt, keeping Ref for
// on-demand fetch.
func linkClues(links []string, rel unit.Relation) []unit.Clue {
	var out []unit.Clue
	for _, l := range links {
		kind := "doc"
		if _, _, ok := language.SplitSymbolID(l); ok {
			kind = "function"
		}
		out = append(out, unit.Clue{Kind: unit.ClueLink, Relation: rel, Text: l + " (" + kind + ")", Ref: l})
	}
	return out
}
