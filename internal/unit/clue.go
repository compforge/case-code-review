package unit

// A review unit's context is assembled on two orthogonal axes (see
// docs/unit-model.md): the Relation (which related symbol a clue came from)
// and the Clue's Kind (what evidence it is). Keeping them separate lets dry-run
// report a relation×kind matrix and lets a clue be labelled by how it was reached.

// ClueKind is what evidence a Clue carries — independent of how it was reached.
type ClueKind string

const (
	ClueSpec ClueKind = "spec" // contract + cases bound to the symbol (spec.json)
	ClueRule ClueKind = "rule" // review criterion (@rule)
	ClueLink ClueKind = "link" // curated see-also pointer (@link)
	ClueDoc  ClueKind = "doc"  // symbol docstring, read from source (adoption-free)
	// ClueProject points at a changed manifest or lockfile belonging to the
	// Unit's Component. It remains on-demand context rather than a review target.
	ClueProject ClueKind = "project"
	// ClueHistory carries a prior review's findings on this symbol, so the reviewer
	// can judge whether the current change addresses them (the review-history
	// feedback loop).
	ClueHistory ClueKind = "history"
)

// Relation is how the symbol a clue came from connects to the changed unit.
type Relation string

const (
	RelSelf    Relation = "self"    // the changed symbol itself
	RelOwner   Relation = "owner"   // its enclosing type/func (a method's class)
	RelCaller  Relation = "caller"  // a caller (upstream to the governing spec)
	RelCallee  Relation = "callee"  // a callee (a depended-on contract)
	RelUsed    Relation = "used"    // a type/func the diff references
	RelProject Relation = "project" // a project file in the same Component
)

// Clue is one piece of review context reached for a Unit: a contract, a review
// rule, a see-also pointer, a docstring, etc. Kind is what it is; Relation is how
// it was reached; Text is the RAW content (no relation wording — the prompt
// renderer labels a clue from relation+kind+Ref, so dedup compares content and
// label phrasing lives in one place); Ref is the source identity (a symbol-id,
// fqn, or doc path), also used to fetch on demand. Finding is separated from
// rendering: a ClueFinder produces Clues, the review prompt renders them.
type Clue struct {
	Kind     ClueKind
	Relation Relation
	Text     string
	Ref      string
}

// Dossier is a Unit's assembled, deduped set of Clues — the review loop's evidence
// input (docs/unit-model.md).
type Dossier = []Clue

// ClueFinder finds the Clues relevant to reviewing one Unit — one implementation
// per (relation, source) it covers. Returning nil is fine. An expensive finder's
// bounding strategy (e.g. how far to walk callers) lives inside the finder.
type ClueFinder interface {
	Find(u Unit) Dossier
}
