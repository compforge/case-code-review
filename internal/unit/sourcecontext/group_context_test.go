package sourcecontext

import (
	"reflect"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

func TestCallerContextDoesNotDependOnUnitScope(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.go": "package p\nfunc Entry(){Helper()}\n", "b.go": "package p\nfunc Helper(){}\n"})
	f := CallerFinder{RepoDir: repo, Index: spec.Index{"a.go::Entry": {Spec: "entry contract"}}, Kinds: spec.KindGates{Spec: true}}
	u := unit.UnitOf(unit.Fragment{Path: "b.go", Symbols: []string{"b.go::Helper"}})
	want := f.Find(u)
	if len(want) != 1 {
		t.Fatal(want)
	}
	for _, scope := range []unit.Scope{unit.ScopeFile, unit.ScopeRelated, unit.ScopeCallChain} {
		u.Scope = scope
		if got := f.Find(u); !reflect.DeepEqual(got, want) {
			t.Fatalf("scope %s changes evidence: %+v", scope, got)
		}
	}
}
func TestOwnContractDoesNotSuppressOtherMembersCallerContract(t *testing.T) {
	repo := newRepo(t, map[string]string{"a.go": "package p\nfunc Entry(){A();B()}\nfunc A(){}\nfunc B(){}\n"})
	f := CallerFinder{RepoDir: repo, Index: spec.Index{"a.go::Entry": {Spec: "governing"}, "a.go::A": {Spec: "own"}}, Kinds: spec.KindGates{Spec: true}}
	u := unit.NewRelatedUnit([]unit.Fragment{{Path: "a.go", Symbols: []string{"a.go::A", "a.go::B"}}})
	clues := f.Find(u)
	if len(clues) != 1 || clues[0].Ref != "a.go::Entry" {
		t.Fatalf("B lost caller context: %+v", clues)
	}
}
