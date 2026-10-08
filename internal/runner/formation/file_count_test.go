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
	seen := map[string]bool{}
	for _, u := range units {
		for _, f := range u.Fragments {
			id := unit.FragmentID(f)
			if !seen[id] {
				files[f.Path] = append(files[f.Path], f)
				seen[id] = true
			}
		}
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
			us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, map[string]string{"app.go": text}), CallChain: related, MaxUnits: 1})
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

func TestCrossFileExtractionPreservesCallGroupsAndCountAllowance(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example\n",
		"a.go":   "package p\nfunc A(){ B() }\nfunc C(){ D() }\nfunc Other(){}\nfunc Extra(){}\n",
		"b.go":   "package p\nfunc B(){}\nfunc D(){}\nfunc Another(){}\n",
	}
	changes := []change.Change{addedFile("a.go", files["a.go"]), addedFile("b.go", files["b.go"])}
	analyzer := graphRepo(t, files)
	config := Config{Changes: changes, Analyzer: analyzer, CallChain: true, MaxUnits: 2}
	us, err := Form(config)
	if err != nil {
		t.Fatal(err)
	}
	assertCoverage(t, changes, us)
	if len(us) > 2 {
		t.Fatalf("extracted groups exceeded the two-file allowance: %+v", us)
	}
	for _, pair := range [][2]string{{"a.go::A", "b.go::B"}, {"a.go::C", "b.go::D"}} {
		a, b := targetUnit(t, us, pair[0]), targetUnit(t, us, pair[1])
		if a.ID != b.ID || len(a.Grouping) == 0 {
			t.Fatalf("file merge lost a graph-backed call group: %v", pair)
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

func TestFileUnitsRetainRenamedAndDeletedEdits(t *testing.T) {
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

func TestFileMergeBudgetRetainsCrossUnitCallEvidence(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example\n",
		"a.go":   "package p\nfunc A(){ B() }\nfunc Other(){}\n",
		"b.go":   "package p\nfunc B(){}\nfunc Another(){}\n",
	}
	changes := []change.Change{addedFile("a.go", files["a.go"]), addedFile("b.go", files["b.go"])}
	us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files), CallChain: true, GroupDiffTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	assertCoverage(t, changes, us)
	for _, symbol := range []string{"a.go::A", "b.go::B"} {
		u := targetUnit(t, us, symbol)
		if len(u.Boundaries) == 0 || len(u.Clues) == 0 || !u.BudgetExceeded {
			t.Fatalf("lost boundary for %s: %+v", symbol, u)
		}
	}
	for _, u := range us {
		if len(u.Paths()) != 1 {
			t.Fatal("budget allowed a cross-file merge")
		}
	}

}

func TestDisablingGraphGroupingKeepsOneUnitPerFile(t *testing.T) {
	files := map[string]string{"a.go": "package p\nfunc A(){B()}\nfunc Other(){}\n", "b.go": "package p\nfunc B(){}\n"}
	changes := []change.Change{addedFile("a.go", files["a.go"]), addedFile("b.go", files["b.go"])}
	us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files)})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 2 {
		t.Fatalf("disabled grouping must keep file scopes: %+v", us)
	}
	assertCoverage(t, changes, us)
}

func TestUnchangedCalleeDoesNotMergeUnrelatedEditsInItsFile(t *testing.T) {
	files := map[string]string{
		"go.mod": "module example\n",
		"a.go":   "package p\nfunc A(){B()}\n",
		"b.go":   "package p\nfunc B(){}\nfunc Other(){}\n",
	}
	changes := []change.Change{
		edit("a.go", files["a.go"], 2, "func A(){}", "func A(){B()}"),
		edit("b.go", files["b.go"], 3, "func Other(){panic(0)}", "func Other(){}"),
	}
	us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files), CallChain: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 2 {
		t.Fatalf("the call target was not changed; files must stay separate: %+v", us)
	}
	assertCoverage(t, changes, us)
}

func TestOnlyChangedCallerAndCalleeFormOneUnit(t *testing.T) {
	files := map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){B()}\n", "b.go": "package p\nfunc B(){}\n"}
	changes := []change.Change{edit("a.go", files["a.go"], 2, "func A(){}", "func A(){B()}"), edit("b.go", files["b.go"], 2, "func B(){panic(0)}", "func B(){}")}
	us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files), CallChain: true, MaxUnits: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 || len(us[0].Fragments) != 2 || len(us[0].Grouping) == 0 {
		t.Fatalf("caller and callee should be the only Unit: %+v", us)
	}
	assertCoverage(t, changes, us)
}
