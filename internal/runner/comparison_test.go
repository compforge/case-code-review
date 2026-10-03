package runner

import (
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"testing"
)

func TestChangeDigestIgnoresOrderButIncludesCapturedMaterial(t *testing.T) {
	a := change.Change{NewPath: "a.go", NewFileContent: "new", OldFileContent: "old", OldContentKnown: true}
	b := change.Change{NewPath: "b.go", Diff: "patch"}
	want := changeDigest([]change.Change{a, b})
	if got := changeDigest([]change.Change{b, a}); got != want {
		t.Fatalf("order changed digest: %s != %s", got, want)
	}
	cases := []change.Change{a, a, a, a}
	cases[0].OldFileContent = "different"
	cases[1].NewFileContent = "different"
	cases[2].BeforeRef = "other-parent"
	cases[3].OldContentKnown = false
	for _, changed := range cases {
		if changeDigest([]change.Change{changed, b}) == want {
			t.Fatalf("material change did not change digest: %+v", changed)
		}
	}
}
