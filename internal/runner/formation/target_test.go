package formation

import (
	"fmt"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"testing"
)

func TestConfiguredGroupingCeiling(t *testing.T) {
	files := map[string]string{"go.mod": "module example\n", "a.go": "package p\nfunc A(){}\nfunc Extra(){}\n", "b.go": "package p\nfunc B(){}\n"}
	changes := []change.Change{addedFile("a.go", files["a.go"]), addedFile("b.go", files["b.go"])}
	for _, tc := range []struct {
		name                string
		configured, ceiling int
	}{
		{"default", 0, 2}, {"below files", 1, 1}, {"equal files", 2, 2}, {"above files", 5, 2},
	} {
		for _, related := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/related=%v", tc.name, related), func(t *testing.T) {
				var report GroupingReport
				us, err := Form(Config{Changes: changes, Analyzer: graphRepo(t, files), CallChain: related, MaxUnits: tc.configured, OnGrouped: func(r GroupingReport) { report = r }})
				want := 2
				if related {
					want = tc.ceiling
				}
				if err != nil || report.MaxUnits != tc.ceiling || report.InitialUnits != 2 || report.LimitExceeded != (want > tc.ceiling) || len(us) != want {
					t.Fatalf("units=%d report=%+v err=%v", len(us), report, err)
				}
				assertCoverage(t, changes, us)
			})
		}
	}
}
