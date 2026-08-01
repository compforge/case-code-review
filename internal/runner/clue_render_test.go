package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestDedupClues(t *testing.T) {
	in := []unit.Clue{
		{Kind: unit.ClueSpec, Relation: unit.RelSelf, Text: "contract"},
		{Kind: unit.ClueSpec, Relation: unit.RelSelf, Text: "contract"},  // exact dup → dropped
		{Kind: unit.ClueSpec, Relation: unit.RelOwner, Text: "contract"}, // same text, diff relation → kept
		{Kind: unit.ClueRule, Relation: unit.RelUsed, Text: "per-request"},
	}
	got := dedupClues(in)
	if len(got) != 3 {
		t.Fatalf("want 3 after dedup, got %d: %+v", len(got), got)
	}
}

// clueLabel is the relation×kind label table: raw clue Text gets its "how it
// reached the unit" wording at render time.
func TestClueLabel_RelationKindTable(t *testing.T) {
	cases := []struct {
		c    unit.Clue
		want string
	}{
		{unit.Clue{Kind: unit.ClueRule, Relation: unit.RelSelf, Ref: "a.py::f"}, ""},
		{unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelOwner, Ref: "a.py::Svc"}, ""}, // Render self-identifies
		{unit.Clue{Kind: unit.ClueRule, Relation: unit.RelOwner, Ref: "a.py::Svc"}, "(enclosing type `Svc`) "},
		{unit.Clue{Kind: unit.ClueDoc, Relation: unit.RelOwner, Ref: "a.py::Svc"}, "enclosing type `Svc` (docstring): "},
		{unit.Clue{Kind: unit.ClueRule, Relation: unit.RelUsed, Ref: "Middleware"}, "(used type `Middleware`) "},
		{unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelUsed, Ref: "Middleware"}, "(used type `Middleware`) "},
		{unit.Clue{Kind: unit.ClueDoc, Relation: unit.RelUsed, Ref: "fw.mw.Middleware"}, "used type `fw.mw.Middleware` (docstring): "},
		{unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelCaller, Ref: "h.go::Handle"}, "(governing spec inherited from caller h.go::Handle)\n"},
		{unit.Clue{Kind: unit.ClueSpec, Relation: unit.RelCallee, Ref: "v.go::validate"}, "(depends on callee v.go::validate, which guarantees)\n"},
		{unit.Clue{Kind: unit.ClueDoc, Relation: unit.RelCallee, Ref: "v.go::validate"}, "callee `v.go::validate` (docstring): "},
	}
	for _, tc := range cases {
		if got := clueLabel(tc.c); got != tc.want {
			t.Errorf("clueLabel(%s/%s) = %q, want %q", tc.c.Relation, tc.c.Kind, got, tc.want)
		}
	}
}

func TestCountClues_RelationKindMatrix(t *testing.T) {
	d := []unit.Clue{
		{Kind: unit.ClueSpec, Relation: unit.RelSelf},
		{Kind: unit.ClueRule, Relation: unit.RelOwner},
		{Kind: unit.ClueDoc, Relation: unit.RelUsed},
		{Kind: unit.ClueSpec, Relation: unit.RelCaller},
	}
	m := countClues(d)
	for _, cell := range []string{"self/spec", "owner/rule", "used/doc", "caller/spec"} {
		if m[cell] != 1 {
			t.Errorf("matrix cell %q = %d, want 1 (got %v)", cell, m[cell], m)
		}
	}
}
