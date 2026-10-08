package formation

import (
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"strings"
	"testing"
)

func TestNamespaceGroupingStopsAtSelectedFileTarget(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		paths []string
		want  int
	}{
		{"go package with imports", map[string]string{"go.mod": "module example\n", "a.go": "package p\nimport \"fmt\"\nfunc A(){fmt.Println()}\n", "b.go": "package p\nimport \"os\"\nfunc B(){os.Exit(0)}\n"}, []string{"a.go", "b.go"}, 2},
		{"go package", map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){}\n", "b.go": "package p\nfunc B(){}\n"}, []string{"a.go", "b.go"}, 2},
		{"go packages share known module", map[string]string{"go.mod": "module example\n", "one/a.go": "package p\nfunc A(){}\n", "two/b.go": "package p\nfunc B(){}\n"}, []string{"one/a.go", "two/b.go"}, 2},
		{"go equal names without module", map[string]string{"one/a.go": "package p\nfunc A(){}\n", "two/b.go": "package p\nfunc B(){}\n"}, []string{"one/a.go", "two/b.go"}, 2},
		{"go independent nested modules", map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){}\n", "nested/go.mod": "module example/nested\n", "nested/b.go": "package p\nfunc B(){}\n"}, []string{"a.go", "nested/b.go"}, 2},
		{"python package", map[string]string{"pkg/__init__.py": "", "pkg/a.py": "def a():\n    pass\n", "pkg/b.py": "def b():\n    pass\n"}, []string{"pkg/a.py", "pkg/b.py"}, 2},
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
			if err != nil || len(us) != tc.want || report.MaxUnits != len(tc.paths) || report.LimitExceeded {
				t.Fatalf("units=%d report=%+v err=%v", len(us), report, err)
			}
			assertCoverage(t, changes, us)
			if len(report.Steps) == 0 || report.Steps[len(report.Steps)-1].Strategy == "namespace" {
				t.Fatalf("steps=%+v", report.Steps)
			}
			if len(report.Steps[len(report.Steps)-1].Merges) != 0 {
				t.Fatalf("unexpected namespace merge after target reached: %+v", report)
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
	if err != nil || len(us) != 3 || report.MaxUnits != 3 || report.LimitExceeded {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	assertCoverage(t, changes, us)
	cfg.GroupDiffTokens = 1
	us, err = Form(cfg)
	if err != nil || len(us) != 3 || report.MaxUnits != 3 || report.LimitExceeded || !us[0].BudgetExceeded {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	assertCoverage(t, changes, us)
}

func TestDeletedFilesCountTowardsGroupingTarget(t *testing.T) {
	files := map[string]string{"a.go": "package p\nfunc A(){}\n", "b.go": "package p\nfunc B(){}\n"}
	var changes []change.Change
	for _, path := range []string{"a.go", "b.go"} {
		changes = append(changes, change.Change{OldPath: path, NewPath: "/dev/null", IsDeleted: true, OldContentKnown: true, OldFileContent: files[path], Diff: "@@ -1,2 +0,0 @@\n-" + strings.ReplaceAll(strings.TrimSuffix(files[path], "\n"), "\n", "\n-") + "\n", Deletions: 2})
	}
	var report GroupingReport
	us, err := Form(Config{Changes: changes, Before: graphRepo(t, files), MaxUnits: 1, OnGrouped: func(r GroupingReport) { report = r }})
	if err != nil || len(us) != 2 || report.MaxUnits != 2 || report.LimitExceeded {
		t.Fatalf("units=%d report=%+v err=%v", len(us), report, err)
	}
	assertCoverage(t, changes, us)
}

func TestNamespaceHierarchyDoesNotOverrideReachedTarget(t *testing.T) {
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
	if err != nil || len(us) != 3 || report.MaxUnits != 3 || len(report.Steps) == 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if len(report.Steps[len(report.Steps)-1].Merges) != 0 {
		t.Fatalf("namespace merged after target reached: %+v", report)
	}
	assertCoverage(t, changes, us)
	cfg.MaxUnits = 1
	us, err = Form(cfg)
	if err != nil || len(us) != 3 || report.MaxUnits != 3 || len(report.Steps) == 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	merges := report.Steps[len(report.Steps)-1].Merges
	if len(merges) != 0 {
		t.Fatalf("namespace merged after target reached: %+v", merges)
	}
	assertCoverage(t, changes, us)
}
