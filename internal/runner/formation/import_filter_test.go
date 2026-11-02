package formation

import (
	"context"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func TestSkipFinalImportOnlyUnits(t *testing.T) {
	for _, tc := range []struct {
		name, before, after, diff string
		want                      int
	}{
		// Known policy limitation: even registration-only changes are skipped.
		{"side-effect import", "package p\n", "package p\nimport _ \"net/http/pprof\"\n", "@@ -1,0 +2 @@\n+import _ \"net/http/pprof\"\n", 0},
		{"added import", "package p\n", "package p\nimport \"fmt\"\n", "@@ -1,0 +2 @@\n+import \"fmt\"\n", 0},
		{"removed import", "package p\nimport \"fmt\"\n", "package p\n", "@@ -2 +1,0 @@\n-import \"fmt\"\n", 0},
		{"changed import", "package p\nimport \"fmt\"\n", "package p\nimport \"os\"\n", "@@ -2 +2 @@\n-import \"fmt\"\n+import \"os\"\n", 0},
		{"code on before side", "package p\nfunc F(){}\n", "package p\nimport \"fmt\"\n", "@@ -2 +2 @@\n-func F(){}\n+import \"fmt\"\n", 1},
		{"code on after side", "package p\nimport \"fmt\"\n", "package p\nfunc F(){}\n", "@@ -2 +2 @@\n-import \"fmt\"\n+func F(){}\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changes := []change.Change{{OldPath: "a.go", NewPath: "a.go", OldContentKnown: true, OldFileContent: tc.before, NewFileContent: tc.after, Diff: tc.diff}}
			var report GroupingReport
			var steps []GroupingStep
			us, err := Form(Config{Changes: changes, Before: graphRepo(t, map[string]string{"a.go": tc.before}), Analyzer: graphRepo(t, map[string]string{"a.go": tc.after}), OnGrouped: func(r GroupingReport) { report = r }, OnStep: func(_ context.Context, s GroupingStep) { steps = append(steps, s) }})
			if err != nil || len(us) != tc.want || report.FinalUnits != tc.want || report.LimitExceeded {
				t.Fatalf("units=%+v report=%+v err=%v", us, report, err)
			}
			if tc.want == 0 {
				last := steps[len(steps)-1]
				if last.Strategy != "skip_import_only" || last.InputUnits != 1 || last.OutputUnits != 0 {
					t.Fatalf("missing filter evidence: %+v", steps)
				}
			} else {
				assertCoverage(t, changes, us)
			}
		})
	}
}

func TestUnknownUnitsRemainReviewable(t *testing.T) {
	changes := []change.Change{addedFile("data.txt", "unclassified content\n")}
	us, err := Form(Config{Changes: changes})
	if err != nil || len(us) != 1 || !us[0].Repo.Counts.Only(repokit.ElementUnknown) {
		t.Fatalf("units=%+v err=%v", us, err)
	}
	assertCoverage(t, changes, us)
}

func TestImportCountEqualsTotalCount(t *testing.T) {
	for _, tc := range []struct {
		name   string
		counts repokit.ElementCounts
		want   bool
	}{
		{"empty", repokit.ElementCounts{}, true},
		{"zero", repokit.ElementCounts{After: map[repokit.ElementKind]int{repokit.ElementUnknown: 0}}, true},
		{"import", repokit.ElementCounts{Before: map[repokit.ElementKind]int{repokit.ElementImport: 1}, After: map[repokit.ElementKind]int{repokit.ElementImport: 2}}, true},
		{"unknown", repokit.ElementCounts{Before: map[repokit.ElementKind]int{repokit.ElementUnknown: 1}, After: map[repokit.ElementKind]int{repokit.ElementImport: 1}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := onlyImportElements(tc.counts); got != tc.want {
				t.Fatalf("onlyImportElements(%+v) = %v, want %v", tc.counts, got, tc.want)
			}
		})
	}
}
