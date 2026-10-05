package formation

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func addedFile(path, text string) change.Change {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	return change.Change{NewPath: path, IsNew: true, NewFileContent: text,
		Diff: fmt.Sprintf("@@ -0,0 +1,%d @@\n+%s\n", len(lines), strings.Join(lines, "\n+")), Insertions: int64(len(lines))}
}

func assertCoverage(t *testing.T, changes []change.Change, units []unit.Unit) {
	t.Helper()
	files := map[string][]unit.Fragment{}
	for _, u := range units {
		for _, f := range u.Fragments {
			files[f.Path] = append(files[f.Path], f)
		}
	}
	if len(units) > len(files) {
		t.Fatalf("%d Units exceed %d target files", len(units), len(files))
	}
	for _, d := range changes {
		if err := validateEdits(d, files[d.Path()]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestImportsAndResidualsDoNotMultiplyReviewLoops(t *testing.T) {
	text := "package p\nimport (\n"
	for i := 0; i < 12; i++ {
		text += fmt.Sprintf("    alias%d \"example/lib%d\"\n", i, i)
	}
	text += ")\nfunc Run(){}\nfunc Other(){}\n"
	changes := []change.Change{addedFile("app.go", text)}
	for _, related := range []bool{false, true} {
		t.Run(fmt.Sprint(related), func(t *testing.T) {
			us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, map[string]string{"app.go": text}), CallChain: related})
			if err != nil {
				t.Fatal(err)
			}
			if len(us) != 1 || len(us[0].Fragments) < 12 || len(us[0].AllSymbols()) < 2 {
				t.Fatalf("imports, residuals and declarations must share a loop without losing ownership: %+v", us)
			}
			assertCoverage(t, changes, us)
		})
	}
}

func TestFileCountCoalescingPreservesCrossFileCalls(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example\n",
		"a.go":   "package p\nfunc A(){ B() }\nfunc C(){ D() }\nfunc Other(){}\n",
		"b.go":   "package p\nfunc B(){}\nfunc D(){}\nfunc Another(){}\n",
	}
	changes := []change.Change{addedFile("a.go", files["a.go"]), addedFile("b.go", files["b.go"])}
	analyzer := graphRepo(t, files)
	config := Config{Changes: changes, Analyzer: analyzer, CallChain: true}
	us, err := Form(config)
	if err != nil {
		t.Fatal(err)
	}
	assertCoverage(t, changes, us)
	for _, pair := range [][2]string{{"a.go::A", "b.go::B"}, {"a.go::C", "b.go::D"}} {
		a, b := targetUnit(t, us, pair[0]), targetUnit(t, us, pair[1])
		if a.ID != b.ID || len(a.Grouping) == 0 {
			t.Fatalf("file-count coalescing broke a graph-backed call group: %v", pair)
		}
	}
	config.Changes = []change.Change{changes[1], changes[0]}
	again, err := Form(config)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(units []unit.Unit) []string {
		var out []string
		for _, u := range units {
			out = append(out, u.ID)
		}
		return out
	}
	if !reflect.DeepEqual(ids(us), ids(again)) {
		t.Fatalf("input order changed grouping: %v / %v", ids(us), ids(again))
	}
}

func TestFileCountCoalescingRetainsRenamedAndDeletedEdits(t *testing.T) {
	files := map[string]string{"new.go": "package p\nfunc A(){}\nfunc B(){}\n"}
	renamed := addedFile("new.go", files["new.go"])
	renamed.IsNew, renamed.IsRenamed, renamed.OldPath = false, true, "old.go"
	deleted := change.Change{OldPath: "gone.go", NewPath: "/dev/null", IsDeleted: true,
		OldContentKnown: true, OldFileContent: "package p\nfunc Gone(){}\n",
		Diff: "@@ -1,2 +0,0 @@\n-package p\n-func Gone(){}\n", Deletions: 2}
	changes := []change.Change{renamed, deleted}
	us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files), CallChain: true})
	if err != nil {
		t.Fatal(err)
	}
	assertCoverage(t, changes, us)
	for _, u := range us {
		for _, f := range u.Fragments {
			if f.Path == "new.go" && f.OldPath != "old.go" {
				t.Fatal("rename identity lost")
			}
			if f.Path == "/dev/null" {
				t.Fatal("deleted file identity lost")
			}
		}
	}
}
