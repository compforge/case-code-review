// Package unit models changed source fragments and their bounded review scope.
// CodeGraph supplies source ownership and relations; formation selects which
// changes to review together, then gathers context for the resulting Unit.
package unit

import (
	"strings"

	"github.com/compforge/go-stdx/slicesx"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// Scope is how a Unit's Fragments were grouped — set when the Unit is formed.
type Scope string

const (
	// ScopeFile groups a whole file's change (residual / unparseable / coalesced).
	ScopeFile Scope = "file"
	// ScopeFunc is a single function's change.
	ScopeFunc Scope = "func"
	// ScopeCallChain groups call-adjacent changed functions (may span files).
	ScopeCallChain Scope = "callchain"
	ScopeRelated   Scope = "related"
)

// Formation records why a Unit has its shape. Scope is presentation only;
// neither field controls the availability of source context.
type Formation string

const (
	FormedFunc     Formation = "func"     // a lone function fragment
	FormedFile     Formation = "file"     // whole-file fragment: residual / unparseable / multi-symbol
	FormedCoalesce Formation = "coalesce" // cost governor merged a file's fragments
	FormedGraph    Formation = "graph"
)

// Fragment is one file's changed region with separate before/after owners.
// It can cover declarations, bindings, or unbound residual edits. Symbols is
// the after-side review identity projection; before-side finders use Before.
type Fragment struct {
	Path          string
	OldPath       string
	Before, After []language.Anchor
	Gaps          []string
	BeforeView    bool // internal projection for old-side ClueFinders
	Symbols       []string
	Diff          string
	Status        string
	Insertions    int64
	Deletions     int64
}

// Unit is the review scope and the currency of the pipeline: the loop runs once
// per Unit. It groups Fragments and carries the Clues found for that scope.
// change.Change is upstream of this (the Splitter consumes it) and does not flow
// past the split.
type Unit struct {
	// ID is a stable identity for telemetry/span naming.
	ID string
	// Scope is how this Unit's Fragments were grouped.
	Scope Scope
	// Formed is why the Unit has that shape (see Formation).
	Formed Formation
	// Fragments are the changed regions reviewed together (one for a function or
	// file Unit; several across files for a call-chain Unit).
	Fragments []Fragment
	// Clues are the deduped project and language facts assembled for this Unit
	// after formation, across the self/owner/caller/callee/used/project relations.
	// See docs/unit-model.md.
	Clues          []Clue
	Grouping       []GroupingEvidence
	Boundaries     []GroupingEvidence
	DiffTokens     int
	BudgetExceeded bool
	// review is shared by value-copied Units and accumulates immutable evidence
	// plus accepted outputs as the Unit moves through Review 1, Review 2 and Trial.
	review *reviewState
}

// AllSymbols returns every symbol-id this Unit covers across its Fragments — the
// join keys for spec/case/history lookup.
func (u Unit) AllSymbols() []string {
	var out []string
	for _, f := range u.Fragments {
		out = append(out, f.Symbols...)
	}
	return out
}

// Insertions / Deletions sum the change across the Unit's Fragments — sizing the
// Unit by its own change, not any owning file's.
func (u Unit) Insertions() int64 {
	return sumFrag(u.Fragments, func(f Fragment) int64 { return f.Insertions })
}
func (u Unit) Deletions() int64 {
	return sumFrag(u.Fragments, func(f Fragment) int64 { return f.Deletions })
}

func sumFrag(fs []Fragment, pick func(Fragment) int64) int64 {
	var n int64
	for _, f := range fs {
		n += pick(f)
	}
	return n
}

// Path is the Unit's primary file (its first Fragment) — used for telemetry and
// as the comment-anchor default. A call-chain Unit spans files; per-comment paths
// (finding.Finding.Path) place findings, so this is only a label.
func (u Unit) Path() string {
	if len(u.Fragments) > 0 {
		return u.Fragments[0].Path
	}
	return ""
}

// Paths returns each distinct member file path (for the change-files exclusion).
func (u Unit) Paths() []string {
	var paths []string
	for _, f := range u.Fragments {
		if f.Path != "" {
			paths = append(paths, f.Path)
		}
	}
	return slicesx.Uniq(paths)
}

// Diff is the diff the Unit reviews: a single Fragment's slice as-is, or — for a
// multi-file Unit — the members concatenated, each under a `// <path>` header so
// the reviewer can tell them apart.
func (u Unit) Diff() string {
	if len(u.Fragments) == 1 {
		return u.Fragments[0].Diff
	}
	var b strings.Builder
	for i, f := range u.Fragments {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("// " + f.Path + "\n" + f.Diff)
	}
	return b.String()
}

// Splitter attributes edits to source owners, retaining unbound residuals.
type Splitter interface {
	Split(d change.Change) ([]Fragment, error)
}

// FileSplitter is the degenerate Splitter: a single whole-file Fragment.
type FileSplitter struct{}

func (FileSplitter) Split(d change.Change) ([]Fragment, error) {
	return []Fragment{{
		Path:       d.Path(),
		OldPath:    d.OldPath,
		Diff:       d.Diff,
		Insertions: d.Insertions,
		Deletions:  d.Deletions,
	}}, nil
}

// UnitOf wraps a single Fragment as its own review Unit: ScopeFunc when it covers
// exactly one function (ID "<path>#<symbol>"), else ScopeFile (ID the path).
func UnitOf(f Fragment) Unit {
	if len(f.Symbols) == 1 {
		return Unit{ID: f.Path + "#" + symbolName(f.Symbols[0]), Scope: ScopeFunc, Formed: FormedFunc, Fragments: []Fragment{f}, review: newReviewState()}
	}
	return Unit{ID: f.Path, Scope: ScopeFile, Formed: FormedFile, Fragments: []Fragment{f}, review: newReviewState()}
}

// symbolName returns the bare symbol from a symbol-id ("p/x.go::Svc.Get" -> "Svc.Get")
// for building a Unit ID; falls back to the whole string when it isn't an id.
func symbolName(symbolID string) string {
	if sym, ok := language.SymbolName(symbolID); ok {
		return sym
	}
	return symbolID
}

// GroupingEvidence explains an accepted relation or a budget boundary between
// changed fragments. It references the source publication rather than owning facts.
type GroupingEvidence struct {
	Before       bool                `json:"before"`
	Link         language.Connection `json:"link"`
	FromFragment string              `json:"from_fragment"`
	ToFragment   string              `json:"to_fragment"`
}
