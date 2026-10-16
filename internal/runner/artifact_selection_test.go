package runner

import (
	"context"
	allowedext "github.com/qiankunli/case-code-review/internal/config/allowlist"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/rules"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func TestArtifactSelectionMatchesPreviewAndRetainsRelatedDiffs(t *testing.T) {
	repo := t.TempDir()
	writeSelectionContent(t, repo, "package.json", "{}\n")
	paths := []string{
		"src/app.ts", "src/schema.generated.ts", "node_modules/lib/index.js",
		"dist/app.js", "package.json", "pnpm-lock.yaml", "doctor-trace.html",
	}
	diffReader := tool.NewFileReadDiff(tool.NewDiffMap(nil))
	registry := tool.NewRegistry()
	registry.Register(diffReader)
	a := &Runner{args: Args{RepoDir: repo, Tools: registry}}
	for _, path := range paths {
		a.changes = append(a.changes, change.Change{NewPath: path, Tags: allowedext.Classify(path), Diff: "diff for " + path})
	}
	prepareSelectionFixture(t, a)
	preview := a.buildPreview()
	if preview.ReviewableCount != 2 || preview.ContextCount != 2 || preview.ExcludedCount != 3 {
		t.Fatalf("unexpected selection counts: %+v", preview)
	}

	// Excluded targets can still explain related source changes through read_diffs.
	a.injectDiffMap()
	a.changes = a.filterDiffs(a.changes)
	selected := make(map[string]bool)
	for _, d := range a.changes {
		selected[effectivePath(d)] = true
	}
	for _, entry := range preview.Entries {
		if entry.WillReview != selected[entry.Path] {
			t.Errorf("preview and actual selection differ for %s", entry.Path)
		}
	}
	text, err := diffReader.Execute(context.Background(), map[string]any{
		"paths": []any{"src/schema.generated.ts", "package.json", "pnpm-lock.yaml"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"src/schema.generated.ts", "package.json", "pnpm-lock.yaml"} {
		if !strings.Contains(text, "diff for "+path) {
			t.Errorf("related diff missing for %s: %s", path, text)
		}
	}
}

func TestArtifactSelectionHonorsUserOverrides(t *testing.T) {
	for _, tt := range []struct {
		name   string
		path   string
		filter *rules.FileFilter
		binary bool
		target bool
		reason ExcludeReason
	}{
		{"default generated exclusion", "schema.generated.ts", nil, false, false, ExcludeDefaultPath},
		{"include generated source", "schema.generated.ts", &rules.FileFilter{Include: []string{"*.generated.ts"}}, false, true, ExcludeNone},
		{"exclude overrides include", "schema.generated.ts", &rules.FileFilter{Include: []string{"*.generated.ts"}, Exclude: []string{"*.generated.ts"}}, false, false, ExcludeUserRule},
		{"include dependency source", "vendor/lib.go", &rules.FileFilter{Include: []string{"vendor/**"}}, false, true, ExcludeNone},
		{"binary still excluded", "schema.generated.ts", &rules.FileFilter{Include: []string{"*.generated.ts"}}, true, false, ExcludeBinary},
		{"project excludes trace report", "doctor-trace.html", &rules.FileFilter{Exclude: []string{"doctor-trace*.html"}}, false, false, ExcludeUserRule},
		{"dependency manifest excluded", "node_modules/lib/package.json", nil, false, false, ExcludeDefaultPath},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			writeSelectionContent(t, repo, "package.json", "{}\n")
			if tt.path == "node_modules/lib/package.json" {
				writeSelectionContent(t, repo, tt.path, "{}\n")
			}
			a := &Runner{
				args:    Args{RepoDir: repo, FileFilter: tt.filter},
				changes: []change.Change{{NewPath: tt.path, Tags: allowedext.Classify(tt.path), IsBinary: tt.binary}},
			}
			prepareSelectionFixture(t, a)
			selection, _ := a.selectionFor(a.changes[0])
			if selection.Target != tt.target || selection.Reason != tt.reason || selection.Context {
				t.Fatalf("unexpected selection: %+v", selection)
			}
		})
	}
}

// +case:id=deleted_diff_evidence,desc=`Read deleted-file evidence by its original path`,expect=`read_diffs returns the captured deletion diff`
func TestInjectDiffMapIncludesDeletedFiles(t *testing.T) {
	changes := []change.Change{
		{NewPath: "added.go", IsNew: true, Diff: "added evidence"},
		{OldPath: "changed.go", NewPath: "changed.go", Diff: "modified evidence"},
		{OldPath: "old.go", NewPath: "renamed.go", IsRenamed: true, Diff: "renamed evidence"},
		{OldPath: "deleted.go", NewPath: "/dev/null", IsDeleted: true, Diff: "deleted evidence"},
	}
	reader := tool.NewFileReadDiff(tool.NewDiffMap(nil))
	registry := tool.NewRegistry()
	registry.Register(reader)
	runner := &Runner{args: Args{Tools: registry}, changes: changes}
	runner.injectDiffMap()
	for _, ch := range changes {
		t.Run(effectivePath(ch), func(t *testing.T) {
			result, err := reader.Execute(t.Context(), map[string]any{"paths": []any{effectivePath(ch)}})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(result, ch.Diff) {
				t.Fatalf("missing captured diff: %s", result)
			}
		})
	}
	result, err := reader.Execute(t.Context(), map[string]any{"paths": []any{"/dev/null"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "deleted evidence") {
		t.Fatal("deletion indexed under /dev/null")
	}
}
