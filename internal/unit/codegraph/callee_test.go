package codegraph

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

func TestCalleeFinder_DependsOnCalleeSpec(t *testing.T) {
	// Svc.Create (the changed func) calls validate, which carries a spec.
	repo := newRepo(t, map[string]string{
		"svc.go":      "package p\n\nfunc (s *Svc) Create(req Req) error {\n\treturn validate(req)\n}\n",
		"validate.go": "package p\n\nfunc validate(req Req) error {\n\treturn nil\n}\n",
	})
	idx, err := spec.Parse([]byte(`{"validate.go::validate": {"spec": "rejects an empty tenant"}}`))
	if err != nil {
		t.Fatal(err)
	}

	u := unit.UnitOf(unit.Fragment{Path: "svc.go", Symbols: []string{"svc.go::Svc.Create"}})
	clues := CalleeFinder{RepoDir: repo, Index: idx, Kinds: spec.KindGates{Spec: true}}.Find(u)

	if len(clues) != 1 {
		t.Fatalf("want 1 callee clue, got %d: %+v", len(clues), clues)
	}
	if clues[0].Kind != unit.ClueSpec || clues[0].Relation != unit.RelCallee || clues[0].Ref != "validate.go::validate" ||
		!strings.Contains(clues[0].Text, "rejects an empty tenant") {
		t.Errorf("clue off: %+v", clues[0])
	}
}

func TestCalleeFinder_Degrades(t *testing.T) {
	u := unit.UnitOf(unit.Fragment{Path: "a.go", Symbols: []string{"a.go::f"}})
	if got := (CalleeFinder{}).Find(u); got != nil {
		t.Errorf("no repo/index should degrade to nil, got %+v", got)
	}
	idx, _ := spec.Parse([]byte(`{"x.go::v": {"spec": "s"}}`))
	file := unit.UnitOf(unit.Fragment{Path: "a.go"})
	if got := (CalleeFinder{RepoDir: t.TempDir(), Index: idx, Kinds: spec.KindGates{Spec: true}}).Find(file); got != nil {
		t.Errorf("file-scope unit should degrade to nil, got %+v", got)
	}
}
