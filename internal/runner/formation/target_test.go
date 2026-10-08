package formation

import (
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"testing"
)

func TestConfiguredGroupingThreshold(t *testing.T) {
	files := map[string]string{"a.go": "package p\nfunc A(){}\nfunc Extra(){}\n", "b.go": "package p\nfunc B(){}\n"}
	changes := []change.Change{addedFile("a.go", files["a.go"]), addedFile("b.go", files["b.go"])}
	for _, tc := range []struct {
		name            string
		threshold, want int
	}{{"default", 0, 2}, {"below files", 1, 2}, {"equal files", 2, 2}, {"above files", 5, 5}} {
		t.Run(tc.name, func(t *testing.T) {
			var report GroupingReport
			us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files), MaxUnits: tc.threshold, OnGrouped: func(r GroupingReport) { report = r }})
			if err != nil || report.MaxUnits != tc.want || report.InitialUnits != 2 || report.LimitExceeded || len(us) != 2 {
				t.Fatalf("units=%d report=%+v err=%v", len(us), report, err)
			}
			assertCoverage(t, changes, us)
		})
	}
}
