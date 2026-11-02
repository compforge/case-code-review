package runner

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/config/rules"
	"github.com/qiankunli/case-code-review/internal/material"
	"github.com/qiankunli/case-code-review/internal/runner/scan"
)

func TestTaggedMaterialExcludedBeforeUnitFormation(t *testing.T) {
	repo := t.TempDir()
	runSelectionGit(t, repo, "init", "-q")
	runSelectionGit(t, repo, "config", "user.email", "test@example.com")
	runSelectionGit(t, repo, "config", "user.name", "Test User")
	writeSelectionContent(t, repo, "go.mod", "module example.org/app\n\ngo 1.26\n")
	runSelectionGit(t, repo, "add", ".")
	runSelectionGit(t, repo, "commit", "-qm", "base")
	paths := map[string]cg.Tag{
		"api.pb.go":              cg.GeneratedTag,
		"runtime.js.map":         cg.GeneratedTag,
		"api.pb.gw.go":           cg.GeneratedTag,
		"api_pb2.py":             cg.GeneratedTag,
		"schema_capnp.py":        cg.GeneratedTag,
		"testdata/data.go":       cg.TestFixtureTag,
		"sub/vendor/lib.go":      cg.DependencyTag,
		"dist/bundle.go":         cg.BuildOutputTag,
		".cache/source.go":       cg.CacheTag,
		"assets/app.min.js":      cg.MinifiedTag,
		"UPPER.PB.GO":            cg.GeneratedTag,
		"sub/.yarn/cache/lib.js": cg.DependencyTag,
		"target/lib.go":          cg.BuildOutputTag,
	}
	for path := range paths {
		writeSelectionContent(t, repo, path, "package app\nfunc Generated() {}\n")
		runSelectionGit(t, repo, "add", "-f", "--", path)
	}
	writeSelectionContent(t, repo, "app.go", "package app\nfunc Work() {}\n")
	writeSelectionContent(t, repo, "go.mod", "module example.org/app\n\ngo 1.26\n// changed metadata\n")
	a := New(Args{RepoDir: repo})
	p, units, _, err := a.DryRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if p.ReviewableCount != 1 || p.ContextCount != 1 || p.ExcludedCount != len(paths) {
		t.Fatalf("selection: %+v", p)
	}
	if len(units) == 0 {
		t.Fatal("lost source Unit")
	}
	for _, u := range units {
		for _, path := range u.Paths {
			if path != "app.go" {
				t.Fatalf("excluded path entered Unit: %s", path)
			}
		}
	}
	seen := 0
	for _, ch := range a.repositoryDiff.Changes {
		if tag, ok := paths[ch.Path()]; ok {
			seen++
			if !slices.Contains(ch.Tags, tag) {
				t.Fatalf("missing %s: %+v", tag, ch)
			}
			if _, err := a.repositoryDiff.ReadSource(false, ch.Path()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if seen != len(paths) {
		t.Fatalf("lost captured changes: %d", seen)
	}
	for _, tt := range []struct {
		filter *rules.FileFilter
		want   bool
	}{
		{&rules.FileFilter{Include: []string{"api.pb.go", "runtime.js.map", "target/**"}}, true},
		{&rules.FileFilter{Include: []string{"api.pb.go"}, Exclude: []string{"api.pb.go"}}, false},
	} {
		b := New(Args{RepoDir: repo, FileFilter: tt.filter})
		_, units, _, err := b.DryRun(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		targetFound := false
		mapFound := false
		for _, u := range units {
			targetFound = targetFound || slices.Contains(u.Paths, "target/lib.go")
			mapFound = mapFound || slices.Contains(u.Paths, "runtime.js.map")
			found = found || slices.Contains(u.Paths, "api.pb.go")
		}
		if tt.want && (!targetFound || !mapFound) {
			t.Fatal("capture dropped explicitly included build output")
		}
		if tt.want {
			for _, ch := range b.repositoryDiff.Changes {
				if ch.Path() == "runtime.js.map" && !slices.Contains(ch.Tags, cg.GeneratedTag) {
					t.Fatal("include removed generated classification")
				}
			}
			if _, ok := b.analyzer.Repository().Graph.Document("runtime.js.map"); ok {
				t.Fatal("review include changed graph admission")
			}
		}
		if found != tt.want {
			t.Fatalf("include/exclude precedence: found=%v want=%v", found, tt.want)
		}
	}
	if err := os.Remove(filepath.Join(repo, "app.go")); err != nil {
		t.Fatal(err)
	}
	onlyExcluded := New(Args{RepoDir: repo})
	p, units, _, err = onlyExcluded.DryRun(context.Background())
	if err != nil || p.ReviewableCount != 0 || len(units) != 0 {
		t.Fatal("excluded-only diff produced units", p, units, err)
	}

}

// +case:id=review_test_material,desc=`Tests retain their classification across review inputs`,expect=`Diff, scan and graph agree while test changes form review Units`
func TestTestSourcesRetainTagsAndFormUnits(t *testing.T) {
	repo := t.TempDir()
	runSelectionGit(t, repo, "init", "-q")
	runSelectionGit(t, repo, "config", "user.email", "test@example.com")
	runSelectionGit(t, repo, "config", "user.name", "Test User")
	writeSelectionContent(t, repo, "go.mod", "module example.org/app\n\ngo 1.26\n")
	runSelectionGit(t, repo, "add", ".")
	runSelectionGit(t, repo, "commit", "-qm", "base")
	sources := map[string]string{
		"app_test.go":     "package app\nfunc TestWork() {}\n",
		"test_service.py": "def test_work():\n    return True\n",
		"service_test.py": "def test_work():\n    return True\n",
		"app.test.ts":     "export function testWork() { return true; }\n",
	}
	for path, body := range sources {
		writeSelectionContent(t, repo, path, body)
	}
	a := New(Args{RepoDir: repo})
	p, units, _, err := a.DryRun(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if p.ReviewableCount != len(sources) {
		t.Fatalf("test targets lost: %+v", p)
	}
	graph := a.analyzer.Repository().Graph
	if graph == nil {
		t.Fatal("missing repository graph")
	}
	selected := map[string]bool{}
	for _, u := range units {
		for _, path := range u.Paths {
			selected[path] = true
		}
	}
	for _, ch := range a.repositoryDiff.Changes {
		path := ch.Path()
		if _, ok := sources[path]; !ok {
			continue
		}
		want := material.Classify(path)
		if !selected[path] || !slices.Contains(ch.Tags, cg.TestTag) || !reflect.DeepEqual(ch.Tags, want) {
			t.Fatalf("test classification/selection lost: %+v, selected=%v", ch, selected)
		}
		node, ok := graph.Document(path)
		if !ok || !reflect.DeepEqual(node.Tags, want) {
			t.Fatalf("graph disagrees for %s: %+v, want %v", path, node, want)
		}
	}
	scanRunner := scan.New(scan.Args{RepoDir: repo})
	view, err := scanRunner.Preview(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range view.Entries {
		if _, ok := sources[entry.Path]; ok {
			seen[entry.Path] = entry.WillReview
		}
	}
	for path := range sources {
		if !seen[path] {
			t.Errorf("scan did not admit test %s", path)
		}
	}
}
