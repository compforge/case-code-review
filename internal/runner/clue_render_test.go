package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
)

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

func TestCluePathsKeepsRelationAndSourcePath(t *testing.T) {
	got := cluePaths([]unit.Clue{
		{Relation: unit.RelCaller, Ref: "caller.go::Handle"},
		{Relation: unit.RelCaller, Ref: "caller.go::Other"},
		{Relation: unit.RelCallee, Ref: "pkg/callee.go::Run"},
		{Relation: unit.RelProject, Ref: "pyproject.toml"},
		{Relation: unit.RelUsed, Ref: "external.Type"},
	})
	if len(got["caller"]) != 1 || got["caller"][0] != "caller.go" {
		t.Fatalf("caller paths = %v, want caller.go once", got["caller"])
	}
	if len(got["callee"]) != 1 || got["callee"][0] != "pkg/callee.go" {
		t.Fatalf("callee paths = %v", got["callee"])
	}
	if len(got["project"]) != 1 || got["project"][0] != "pyproject.toml" {
		t.Fatalf("project paths = %v", got["project"])
	}
	if _, ok := got["used"]; ok {
		t.Fatalf("non-source fqn must not become a file path: %v", got["used"])
	}
}
