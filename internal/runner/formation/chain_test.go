package formation

import (
	"context"
	"errors"
	cg "github.com/compforge/codegraph"
	"reflect"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit/change"
)

type testGrouper struct {
	name string
	run  func(GroupingInput) (GroupingResult, error)
}

func (g testGrouper) Name() string { return g.name }
func (g testGrouper) Group(_ context.Context, in GroupingInput) (GroupingResult, error) {
	return g.run(in)
}

func TestGroupChainRunsPrimaryThenStopsAtTarget(t *testing.T) {
	var ran []string
	pass := func(name string, merge bool) Grouper {
		return testGrouper{name, func(in GroupingInput) (GroupingResult, error) {
			ran = append(ran, name)
			if merge {
				return GroupingResult{Groups: []Group{flattenGroups(in.Groups)}}, nil
			}
			return GroupingResult{Groups: in.Groups}, nil
		}}
	}
	chain := GroupChain{Groupers: []Grouper{pass("relations", false), pass("namespace", true), pass("later", true)}}
	input := GroupingInput{Groups: []Group{{{Path: "a.go"}}, {{Path: "b.go"}}}, MaxUnits: 2}
	_, report, err := chain.Group(context.Background(), input)
	if err != nil || !reflect.DeepEqual(ran, []string{"relations"}) || report.LimitExceeded {
		t.Fatalf("ran=%v report=%+v err=%v", ran, report, err)
	}
	ran = nil
	input.MaxUnits = 1
	_, report, err = chain.Group(context.Background(), input)
	if err != nil || !reflect.DeepEqual(ran, []string{"relations", "namespace"}) || report.FinalUnits != 1 {
		t.Fatalf("ran=%v report=%+v err=%v", ran, report, err)
	}
}

func TestGroupChainRetainsCoverageAndReportsUnattainableTarget(t *testing.T) {
	input := GroupingInput{Groups: []Group{{{Path: "a.go"}}, {{Path: "b.go"}}}, MaxUnits: 1}
	for _, mode := range []string{"drop", "duplicate", "empty"} {
		t.Run(mode, func(t *testing.T) {
			chain := GroupChain{Groupers: []Grouper{testGrouper{mode, func(in GroupingInput) (GroupingResult, error) {
				out := append([]Group(nil), in.Groups...)
				switch mode {
				case "drop":
					out = out[:1]
				case "duplicate":
					out = append(out, out[0])
				case "empty":
					out = append(out, nil)
				}
				return GroupingResult{Groups: out}, nil
			}}}}
			if _, _, err := chain.Group(context.Background(), input); err == nil || !strings.Contains(err.Error(), "coverage") {
				t.Fatalf("err=%v", err)
			}
		})
	}
	chain := GroupChain{Groupers: []Grouper{RelationGrouper{Disabled: true}, NamespaceGrouper{}}}
	result, report, err := chain.Group(context.Background(), input)
	if err != nil || len(result.Groups) != 2 || !report.LimitExceeded || report.Steps[1].MissingNamespace != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}

func TestNamespaceGroupingUsesLanguageOwnership(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		paths []string
		want  int
	}{
		{"go package with imports", map[string]string{"go.mod": "module example\n", "a.go": "package p\nimport \"fmt\"\nfunc A(){fmt.Println()}\n", "b.go": "package p\nimport \"os\"\nfunc B(){os.Exit(0)}\n"}, []string{"a.go", "b.go"}, 1},
		{"go package", map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){}\n", "b.go": "package p\nfunc B(){}\n"}, []string{"a.go", "b.go"}, 1},
		{"go packages share known module", map[string]string{"go.mod": "module example\n", "one/a.go": "package p\nfunc A(){}\n", "two/b.go": "package p\nfunc B(){}\n"}, []string{"one/a.go", "two/b.go"}, 1},
		{"go equal names without module", map[string]string{"one/a.go": "package p\nfunc A(){}\n", "two/b.go": "package p\nfunc B(){}\n"}, []string{"one/a.go", "two/b.go"}, 2},
		{"go independent nested modules", map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){}\n", "nested/go.mod": "module example/nested\n", "nested/b.go": "package p\nfunc B(){}\n"}, []string{"a.go", "nested/b.go"}, 2},
		{"python package", map[string]string{"pkg/__init__.py": "", "pkg/a.py": "def a():\n    pass\n", "pkg/b.py": "def b():\n    pass\n"}, []string{"pkg/a.py", "pkg/b.py"}, 1},
		{"python missing package", map[string]string{"pkg/a.py": "def a():\n    pass\n", "pkg/b.py": "def b():\n    pass\n"}, []string{"pkg/a.py", "pkg/b.py"}, 2},
		{"typescript file modules", map[string]string{"src/a.ts": "export function a() {}\n", "src/b.ts": "export function b() {}\n"}, []string{"src/a.ts", "src/b.ts"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var changes []change.Change
			for _, path := range tc.paths {
				changes = append(changes, addedFile(path, tc.files[path]))
			}
			var report GroupingReport
			cfg := Config{Analyzer: graphRepo(t, tc.files), Changes: changes, CallChain: true, MaxUnits: 1, OnGrouped: func(r GroupingReport) { report = r }}
			us, err := Form(cfg)
			if err != nil || len(us) != tc.want || report.LimitExceeded != (tc.want > 1) {
				t.Fatalf("units=%d report=%+v err=%v", len(us), report, err)
			}
			assertCoverage(t, changes, us)
			if len(report.Steps) != 2 || report.Steps[0].Strategy != "relations" || report.Steps[1].Strategy != "namespace" {
				t.Fatalf("steps=%+v", report.Steps)
			}
			if tc.want == 1 && (len(report.Steps[1].Merges) != 1 || report.Steps[1].Merges[0].Namespace.NodeID == "" || len(report.Steps[1].Merges[0].Paths) < 2) {
				t.Fatalf("missing namespace evidence: %+v", report)
			}
			cfg.Changes = []change.Change{changes[1], changes[0]}
			again, err := Form(cfg)
			if err != nil || len(again) != len(us) {
				t.Fatal(err)
			}
			for i := range us {
				if us[i].ID != again[i].ID {
					t.Fatal("input order changed grouping")
				}
			}
		})
	}
}

func TestNamespaceGroupingStopsAtTargetAndHonorsSizeBudget(t *testing.T) {
	files := map[string]string{"a.go": "package p\nfunc A(){}\n", "b.go": "package p\nfunc B(){}\n", "c.go": "package p\nfunc C(){}\n"}
	var changes []change.Change
	for _, path := range []string{"a.go", "b.go", "c.go"} {
		changes = append(changes, addedFile(path, files[path]))
	}
	var report GroupingReport
	cfg := Config{Analyzer: graphRepo(t, files), Changes: changes, MaxUnits: 2, OnGrouped: func(r GroupingReport) { report = r }}
	us, err := Form(cfg)
	if err != nil || len(us) != 2 || report.LimitExceeded {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	assertCoverage(t, changes, us)
	cfg.GroupDiffTokens = 1
	us, err = Form(cfg)
	if err != nil || len(us) != 3 || !report.LimitExceeded || report.Steps[1].BudgetBlocked == 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	assertCoverage(t, changes, us)
}

func TestNamespaceGroupingKeepsCrossPackageGroupIntact(t *testing.T) {
	files := map[string]string{"one/a.go": "package p\nfunc A(){}\n", "two/b.go": "package q\nfunc B(){}\n", "one/c.go": "package p\nfunc C(){}\n"}
	a := graphRepo(t, files)
	// No anchors exercises the Document contribution path used by residual edits.
	input := GroupingInput{After: a, MaxUnits: 1, DiffTokens: DefaultGroupDiffTokens, Groups: []Group{
		{{Path: "one/a.go"}, {Path: "two/b.go"}}, {{Path: "one/c.go"}},
	}}
	result, err := (NamespaceGrouper{}).Group(context.Background(), input)
	if err != nil || len(result.Groups) != 2 || len(result.Groups[0]) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDeletedFilesUseBeforeNamespace(t *testing.T) {
	files := map[string]string{"a.go": "package p\nfunc A(){}\n", "b.go": "package p\nfunc B(){}\n"}
	var changes []change.Change
	for _, path := range []string{"a.go", "b.go"} {
		changes = append(changes, change.Change{OldPath: path, NewPath: "/dev/null", IsDeleted: true, OldContentKnown: true, OldFileContent: files[path], Diff: "@@ -1,2 +0,0 @@\n-" + strings.ReplaceAll(strings.TrimSuffix(files[path], "\n"), "\n", "\n-") + "\n", Deletions: 2})
	}
	var report GroupingReport
	us, err := Form(Config{Changes: changes, Before: graphRepo(t, files), MaxUnits: 1, OnGrouped: func(r GroupingReport) { report = r }})
	if err != nil || len(us) != 1 || report.LimitExceeded {
		t.Fatalf("units=%d report=%+v err=%v", len(us), report, err)
	}
	assertCoverage(t, changes, us)
}

var _ Grouper = RelationGrouper{}
var _ Grouper = NamespaceGrouper{}

func TestNamespaceGroupingPrefersNearestCommonAncestor(t *testing.T) {
	files := map[string]string{
		"app/__init__.py": "", "app/api/__init__.py": "", "app/db/__init__.py": "",
		"app/api/a.py": "def a():\n    pass\n", "app/api/b.py": "def b():\n    pass\n", "app/db/c.py": "def c():\n    pass\n",
	}
	var changes []change.Change
	for _, path := range []string{"app/api/a.py", "app/api/b.py", "app/db/c.py"} {
		changes = append(changes, addedFile(path, files[path]))
	}
	var report GroupingReport
	cfg := Config{Analyzer: graphRepo(t, files), Changes: changes, MaxUnits: 2, OnGrouped: func(r GroupingReport) { report = r }}
	us, err := Form(cfg)
	if err != nil || len(us) != 2 || len(report.Steps) != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if merge := report.Steps[1].Merges[0]; merge.Namespace.Name != "api" {
		t.Fatalf("nearest namespace not chosen: %+v", merge.Namespace)
	}
	assertCoverage(t, changes, us)
	cfg.MaxUnits = 1
	us, err = Form(cfg)
	if err != nil || len(us) != 1 || len(report.Steps) != 2 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	merges := report.Steps[1].Merges
	if merges[len(merges)-1].Namespace.Name != "app" {
		t.Fatalf("did not ascend to common namespace: %+v", merges)
	}
}

func TestNamespaceGroupingPropagatesQueryFailure(t *testing.T) {
	files := map[string]string{}
	path := ""
	for i := 0; i < 10; i++ {
		path += "p/"
		files[path+"__init__.py"] = ""
	}
	files[path+"a.py"] = "def a():\n    pass\n"
	analyzer := graphRepo(t, files)
	input := GroupingInput{After: analyzer, MaxUnits: 1, Groups: []Group{{{Path: path + "a.py"}}, {{Path: path + "__init__.py"}}}}
	if _, err := (NamespaceGrouper{}).Group(context.Background(), input); !errors.Is(err, cg.ErrQueryBudget) {
		t.Fatalf("query failure hidden: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (NamespaceGrouper{}).Group(ctx, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden: %v", err)
	}
}
