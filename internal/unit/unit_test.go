package unit

import "testing"

func TestUnitOfFileFragment(t *testing.T) {
	f := Fragment{Path: "a.go", Diff: "patch"}
	u := UnitOf(f)
	if u.ID != f.Path || u.Scope != ScopeFile || u.Diff() != f.Diff {
		t.Fatalf("%+v", u)
	}
}
