package codegraph

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

func requirePython3(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
}

func TestCalleeFinder_Python(t *testing.T) {
	requirePython3(t)
	// Svc.create (changed) calls validate, which carries a spec.
	repo := newRepo(t, map[string]string{
		"svc.py":      "class Svc:\n    def create(self, req):\n        return validate(req)\n",
		"validate.py": "def validate(req):\n    return None\n",
	})
	idx, err := spec.Parse([]byte(`{"validate.py::validate": {"spec": "rejects an empty tenant"}}`))
	if err != nil {
		t.Fatal(err)
	}
	u := unit.UnitOf(unit.Fragment{Path: "svc.py", Symbols: []string{"svc.py::Svc.create"}})
	clues := CalleeFinder{RepoDir: repo, Index: idx, Kinds: spec.KindGates{Spec: true}}.Find(u)
	if len(clues) != 1 || clues[0].Kind != unit.ClueSpec || clues[0].Relation != unit.RelCallee ||
		clues[0].Ref != "validate.py::validate" || !strings.Contains(clues[0].Text, "rejects an empty tenant") {
		t.Fatalf("want validate callee clue, got %+v", clues)
	}
}

func TestCalleeFinder_PythonDocstring(t *testing.T) {
	requirePython3(t)
	// The callee has a docstring but no spec.json entry — adoption-free contract.
	repo := newRepo(t, map[string]string{
		"svc.py":      "class Svc:\n    def create(self, req):\n        return validate(req)\n",
		"validate.py": "def validate(req):\n    \"\"\"Rejects an empty tenant.\"\"\"\n    return None\n",
	})
	u := unit.UnitOf(unit.Fragment{Path: "svc.py", Symbols: []string{"svc.py::Svc.create"}})
	clues := CalleeFinder{RepoDir: repo, Kinds: spec.KindGates{Doc: true}}.Find(u)
	if len(clues) != 1 || clues[0].Kind != unit.ClueDoc || clues[0].Relation != unit.RelCallee ||
		clues[0].Ref != "validate.py::validate" || !strings.Contains(clues[0].Text, "Rejects an empty tenant") {
		t.Fatalf("want callee docstring clue, got %+v", clues)
	}
}

func TestCallerFinder_Python(t *testing.T) {
	requirePython3(t)
	// helper (changed, no spec) <- handle (calls helper, spec): inherit upward.
	repo := newRepo(t, map[string]string{
		"helper.py": "def helper(req):\n    return None\n",
		"entry.py":  "def handle(req):\n    return helper(req)\n",
	})
	idx, err := spec.Parse([]byte(`{"entry.py::handle": {"spec": "the governing contract"}}`))
	if err != nil {
		t.Fatal(err)
	}
	u := unit.UnitOf(unit.Fragment{Path: "helper.py", Symbols: []string{"helper.py::helper"}})
	clues := CallerFinder{RepoDir: repo, Index: idx, Kinds: spec.KindGates{Spec: true}}.Find(u)
	if len(clues) != 1 || clues[0].Kind != unit.ClueSpec || clues[0].Relation != unit.RelCaller ||
		clues[0].Ref != "entry.py::handle" {
		t.Fatalf("want inherited handle spec, got %+v", clues)
	}
}
