package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
	"github.com/qiankunli/case-code-review/internal/unit/codegraph"
	"github.com/qiankunli/case-code-review/internal/unit/spec"
)

// TestDogfoodContextAssembly runs the real context pipeline (split → finders →
// merge → render) over a tiny but realistic handler→service chain, and prints
// what ccr would inject for each changed function. Run with:
//
//	go test ./internal/runner -run Dogfood -v
//
// It exercises both paths: a function with its own spec/case/rule/link, and a
// deep function that inherits its caller's spec via CallerFinder (real git grep
// + go/ast). No LLM involved — this shows the assembled context, not a review.
func TestDogfoodContextAssembly(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	// handler.go::CreateNotebook (entry, carries the contract) calls doCreate;
	// service.go::doCreate (deep helper, no spec of its own) is what we change.
	files := map[string]string{
		"handler.go": "package app\n\n" +
			"func CreateNotebook(req Req) error {\n\treturn doCreate(req)\n}\n\n" +
			"func UpdateNotebook(req Req) error {\n\treturn nil\n}\n",
		"service.go": "package app\n\n" +
			"func doCreate(req Req) error {\n\treturn validate(req)\n}\n",
	}
	repo := dogfoodRepo(t, files)

	idx, err := spec.Parse([]byte(`{
	  "handler.go::CreateNotebook": {
	    "spec": "tenant header required; (tenant,name) unique — duplicate create returns ConflictError",
	    "cases": [{"id":"dup","desc":"duplicate name","expect":"409","forbid":"a second row is written"}],
	    "rules": ["request hot path — flag any new synchronous DB call"],
	    "links": ["docs/tenancy.md", "handler.go::UpdateNotebook"]
	  }
	}`))
	if err != nil {
		t.Fatal(err)
	}

	a := &Runner{
		splitter: unit.AutoSplitter{},
		merger:   unit.WatermarkMerger{Watermark: defaultUnitWatermark},
		finders: []unit.ClueFinder{
			spec.NewRelatedFinder(spec.Catalog{Local: idx}, repo, spec.KindGates{Spec: true, Rule: true, Link: true, Doc: true}),
		},
		costlyFinders: []unit.ClueFinder{
			codegraph.CallerFinder{RepoDir: repo, Index: idx, Kinds: spec.KindGates{Spec: true}},
		},
		changes: []change.Change{
			{NewPath: "handler.go", NewFileContent: files["handler.go"], Insertions: 1, Deletions: 1,
				Diff: "@@ -4,1 +4,1 @@\n-\treturn doCreate(req)\n+\treturn doCreate(req) // audited\n"},
			{NewPath: "service.go", NewFileContent: files["service.go"], Insertions: 1, Deletions: 1,
				Diff: "@@ -4,1 +4,1 @@\n-\treturn nil\n+\treturn validate(req)\n"},
		},
	}

	units, err := a.splitUnits()
	if err != nil {
		t.Fatal(err)
	}
	type ctx struct{ sc, ru, sa string }
	got := map[string]ctx{}
	for _, u := range units {
		sc, ru, sa, _ := renderClues(u.Clues)
		got[u.ID] = ctx{sc, ru, sa}
		t.Logf("\n========== review unit: %s ==========\n"+
			"── Governing Spec/Case ──\n%s\n── Review Rules ──\n%s\n── See Also ──\n%s",
			u.ID, orNone(sc), orNone(ru), orNone(sa))
	}

	// The entry function gets all four of its own context paths. (A function
	// Unit's ID is "<path>#<symbol>" — the telemetry id — distinct from the
	// "<path>::<symbol>" symbol-id used as the spec join key.)
	if e := got["handler.go#CreateNotebook"]; !strings.Contains(e.sc, "tenant header") ||
		!strings.Contains(e.ru, "hot path") || !strings.Contains(e.sa, "tenancy.md") {
		t.Errorf("entry unit missing its own spec/rule/link context: %+v", e)
	}
	// The deep helper has no context of its own, yet inherits the caller's spec
	// (and only the spec — rules/links are not inherited).
	if e := got["service.go#doCreate"]; !strings.Contains(e.sc, "inherited from caller handler.go::CreateNotebook") ||
		!strings.Contains(e.sc, "tenant header") || e.ru != "" || e.sa != "" {
		t.Errorf("callee unit should inherit only the caller's spec: %+v", e)
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func dogfoodRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	repo := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		{"add", "-A"}, {"commit", "-m", "x"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}
