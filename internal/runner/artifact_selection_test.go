package runner

import (
	"context"
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
		a.changes = append(a.changes, change.Change{NewPath: path, Diff: "diff for " + path})
	}
	a.prepareFileSelections(context.Background())
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
				changes: []change.Change{{NewPath: tt.path, IsBinary: tt.binary}},
			}
			a.prepareFileSelections(context.Background())
			selection, _ := a.selectionFor(a.changes[0])
			if selection.Target != tt.target || selection.Reason != tt.reason || selection.Context {
				t.Fatalf("unexpected selection: %+v", selection)
			}
		})
	}
}
