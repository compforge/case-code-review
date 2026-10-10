package formation

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func graphRepo(t *testing.T, files map[string]string) *language.Analyzer {
	t.Helper()
	dir := t.TempDir()
	for path, text := range files {
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return language.NewAnalyzer(dir)
}
func edit(path, content string, line int, old, new string) change.Change {
	return change.Change{OldPath: path, NewPath: path, NewFileContent: content, Diff: fmt.Sprintf("@@ -%d +%d @@\n-%s\n+%s\n", line, line, old, new), Insertions: 1, Deletions: 1}
}
func targetUnit(t *testing.T, us []unit.Unit, symbol string) unit.Unit {
	t.Helper()
	for _, u := range us {
		for _, s := range u.AllSymbols() {
			if s == symbol {
				return u
			}
		}
	}
	t.Fatalf("target %s absent", symbol)
	return unit.Unit{}
}
func TestGraphFormationPacksSmallSameFileRemainderAfterChangedCalls(t *testing.T) {
	files := map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){ B() }\nfunc Other(){}\n", "b.go": "package p\nfunc B(){}\n"}
	changes := []change.Change{edit("a.go", files["a.go"], 2, "func A(){}", "func A(){ B() }"), edit("b.go", files["b.go"], 2, "func B(){panic(0)}", "func B(){}")}
	changes[0].Diff += "@@ -3 +3 @@\n-func Other(){panic(0)}\n+func Other(){}\n"
	us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files), CallChain: true})
	if err != nil {
		t.Fatal(err)
	}
	a := targetUnit(t, us, "a.go::A")
	b := targetUnit(t, us, "b.go::B")
	other := targetUnit(t, us, "a.go::Other")
	if len(us) != 1 || a.ID != b.ID || a.ID != other.ID || len(a.Grouping) == 0 {
		t.Fatalf("wrong partition: %+v", us)
	}
	if len(a.Paths()) != 2 {
		t.Fatal(a.Paths())
	}
}
func TestGraphFormationAliasesMergeBelowCountCeiling(t *testing.T) {
	files := map[string]string{"lib.ts": "export const LIMIT = 2;\n", "app.ts": "import { LIMIT as cap } from './lib';\nexport function run(){ return cap; }\n"}
	us, err := Form(Config{Changes: []change.Change{edit("lib.ts", files["lib.ts"], 1, "export const LIMIT = 1;", "export const LIMIT = 2;"), edit("app.ts", files["app.ts"], 2, "export function run(){ return 0; }", "export function run(){ return cap; }")}, Analyzer: graphRepo(t, files), CallChain: true, MaxUnits: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 || len(us[0].Paths()) != 2 || len(us[0].Grouping) == 0 || len(us[0].Boundaries) != 0 {
		t.Fatalf("alias use should join changed declarations with evidence: %+v", us)
	}
}
func TestGraphFormationDoesNotJoinCommonUnchangedDependency(t *testing.T) {
	files := map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){ Shared() }\n", "b.go": "package p\nfunc B(){ Shared() }\n", "shared.go": "package p\nfunc Shared(){}\n"}
	us, err := Form(Config{Changes: []change.Change{edit("a.go", files["a.go"], 2, "func A(){}", "func A(){ Shared() }"), edit("b.go", files["b.go"], 2, "func B(){}", "func B(){ Shared() }")}, Analyzer: graphRepo(t, files), CallChain: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 2 {
		t.Fatalf("shared context is not target coupling: %+v", us)
	}
}
func TestGraphFormationUsesRemovedCallsAndDeletedFiles(t *testing.T) {
	beforeFiles := map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){ Guard() }\n", "guard.go": "package p\nfunc Guard(){}\n"}
	afterFiles := map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){}\n"}
	a := edit("a.go", afterFiles["a.go"], 2, "func A(){ Guard() }", "func A(){}")
	a.OldFileContent = beforeFiles["a.go"]
	a.OldContentKnown = true
	gone := change.Change{OldPath: "guard.go", NewPath: "/dev/null", IsDeleted: true, OldContentKnown: true, OldFileContent: beforeFiles["guard.go"], Diff: "@@ -1,2 +0,0 @@\n-package p\n-func Guard(){}\n", Deletions: 2}
	us, err := Form(Config{Changes: []change.Change{a, gone}, Analyzer: graphRepo(t, afterFiles), Before: graphRepo(t, beforeFiles), CallChain: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := false
	count := 0
	for _, u := range us {
		for _, f := range u.Fragments {
			count += int(f.Deletions)
			if f.Path == "/dev/null" {
				t.Fatal("lost deleted path")
			}
		}
		for _, e := range u.Grouping {
			if e.Before {
				joined = true
			}
		}
	}
	if !joined || count != 3 {
		t.Fatalf("old evidence/coverage lost: %+v", us)
	}
}
func TestGraphFormationPartitionsLargeGraphDeterministically(t *testing.T) {
	files := map[string]string{"go.mod": "module example\n"}
	var changes []change.Change
	for i := 0; i < 24; i++ {
		path := fmt.Sprintf("f%02d.go", i)
		body := fmt.Sprintf("func F%02d(){}", i)
		if i < 23 {
			body = fmt.Sprintf("func F%02d(){F%02d()}", i, i+1)
		}
		files[path] = "package p\n" + body + "\n"
		changes = append(changes, edit(path, files[path], 2, fmt.Sprintf("func F%02d(){panic(0)}", i), body))
	}
	analyzer := graphRepo(t, files)
	cfg := Config{Changes: changes, Analyzer: analyzer, CallChain: true, MaxUnits: 1, GroupDiffTokens: 300}
	us, err := Form(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(us) < (24+maxGroupFiles-1)/maxGroupFiles || len(us) >= 24 {
		t.Fatalf("expected bounded grouping of the caller/callee chain, got %d", len(us))
	}
	covered := map[string]bool{}
	var ids []string
	boundaries := 0
	for _, u := range us {
		ids = append(ids, u.ID)
		boundaries += len(u.Boundaries)
		if len(u.Clues) > 9 {
			t.Fatalf("unbounded cut context: %d", len(u.Clues))
		}
		if len(u.Paths()) > maxGroupFiles || llm.CountTokens(u.Diff()) > 300 {
			t.Fatalf("budget exceeded: %+v", u)
		}
		for _, f := range u.Fragments {
			id := unit.FragmentID(f)
			if covered[id] {
				t.Fatal("duplicate target")
			}
			covered[id] = true
		}
	}
	if len(covered) != 24 || boundaries == 0 {
		t.Fatal("lost coverage or cut relations", len(covered), boundaries)
	}
	for i, j := 0, len(changes)-1; i < j; i, j = i+1, j-1 {
		changes[i], changes[j] = changes[j], changes[i]
	}
	cfg.Changes = changes
	again, err := Form(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var next []string
	for _, u := range again {
		next = append(next, u.ID)
	}
	if !reflect.DeepEqual(ids, next) {
		t.Fatal("input order changed partition", ids, next)
	}
}
func TestGraphFormationFileCountTakesPrecedenceOverTokenBudget(t *testing.T) {
	var added strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&added, "+line_%d_%s\n", i, strings.Repeat("abc ", 20))
	}
	d := change.Change{NewPath: "unknown.txt", IsNew: true, Diff: "@@ -0,0 +1,20 @@\n" + added.String(), Insertions: 20}
	us, err := Form(Config{Changes: []change.Change{d}, GroupDiffTokens: 150})
	if err != nil {
		t.Fatal(err)
	}
	var fs []unit.Fragment
	for _, u := range us {
		fs = append(fs, u.Fragments...)
	}
	if len(us) != 1 || !us[0].BudgetExceeded || us[0].DiffTokens <= 150 {
		t.Fatalf("oversize file must remain one explicitly over-budget Unit: %+v", us)
	}
	if err := validateEdits(d, fs); err != nil {
		t.Fatal(err)
	}
}
func TestGraphFormationMissingGraphRetainsTargets(t *testing.T) {
	d := change.Change{NewPath: "x.unknown", Diff: "@@ -1 +1 @@\n-old\n+new\n"}
	us, err := Form(Config{Changes: []change.Change{d}, CallChain: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 || !strings.Contains(us[0].Diff(), "+new") {
		t.Fatal(us)
	}
}
func TestRelatedUnitIdentityIncludesPathAndPatch(t *testing.T) {
	a := unit.Fragment{Path: "a.go", Symbols: []string{"a.go::Run"}, Diff: "@@ -1 +1 @@\n-a\n+b"}
	b := a
	b.Path = "b.go"
	b.Symbols = []string{"b.go::Run"}
	if unit.NewRelatedUnit([]unit.Fragment{a}).ID == unit.NewRelatedUnit([]unit.Fragment{b}).ID {
		t.Fatal("same-name collision")
	}
	u1 := unit.NewRelatedUnit([]unit.Fragment{a, b})
	u2 := unit.NewRelatedUnit([]unit.Fragment{b, a})
	if u1.ID != u2.ID {
		t.Fatal("unstable id")
	}
	b.Diff += "\n+c"
	if u1.ID == unit.NewRelatedUnit([]unit.Fragment{a, b}).ID {
		t.Fatal("different patches share id")
	}
}

// Readable replay output uses sorted target paths, while identity remains opaque.
func TestFormationReplaySummary(t *testing.T) {
	files := map[string]string{"a.go": "package p\nfunc A(){B()}\n", "b.go": "package p\nfunc B(){}\n"}
	us, err := Form(Config{Analyzer: graphRepo(t, files), CallChain: true, Changes: []change.Change{edit("a.go", files["a.go"], 2, "func A(){}", "func A(){B()}"), edit("b.go", files["b.go"], 2, "func B(){panic(0)}", "func B(){}")}})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range us {
		paths := u.Paths()
		sort.Strings(paths)
		t.Logf("paths=%v targets=%d relations=%d diff_tokens=%d", paths, len(u.Fragments), len(u.Grouping), u.DiffTokens)
	}
}

func TestIndivisibleOversizeTargetIsExplicit(t *testing.T) {
	d := change.Change{NewPath: "large.txt", Diff: "@@ -0,0 +1 @@\n+" + strings.Repeat("abc ", 200) + "\n", Insertions: 1}
	us, err := Form(Config{Changes: []change.Change{d}, GroupDiffTokens: 40})
	if err != nil {
		t.Fatal(err)
	}
	if len(us) != 1 || !us[0].BudgetExceeded || !strings.Contains(us[0].Diff(), strings.Repeat("abc ", 200)) {
		t.Fatalf("oversize target silently accepted: %+v", us)
	}
}

func TestDocumentationOnlyEditKeepsDeclarationOwnership(t *testing.T) {
	content := "package p\n// Guard requires authorization.\nfunc Guard(){}\n"
	d := edit("guard.go", content, 2, "// Guard accepts everyone.", "// Guard requires authorization.")
	us, err := Form(Config{Changes: []change.Change{d}, Analyzer: graphRepo(t, map[string]string{"guard.go": content}), CallChain: true})
	if err != nil {
		t.Fatal(err)
	}
	u := targetUnit(t, us, "guard.go::Guard")
	if len(u.Fragments) != 1 || len(u.Fragments[0].After) != 1 {
		t.Fatalf("documentation lost its graph owner: %+v", u)
	}
}
