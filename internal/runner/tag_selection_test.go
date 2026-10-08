package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	cg "github.com/compforge/codegraph"
	allowedext "github.com/qiankunli/case-code-review/internal/config/allowlist"
	"github.com/qiankunli/case-code-review/internal/config/rules"
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
		"testdata/data.go":       cg.TestFixtureTag,
		"sub/vendor/lib.go":      cg.DependencyTag,
		"dist/bundle.go":         cg.BuildOutputTag,
		".cache/source.go":       cg.CacheTag,
		"assets/app.min.js":      cg.MinifiedTag,
		"UPPER.PB.GO":            cg.GeneratedTag,
		"a_test.go":              allowedext.TestTag,
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
		{&rules.FileFilter{Include: []string{"api.pb.go", "target/**"}}, true},
		{&rules.FileFilter{Include: []string{"api.pb.go"}, Exclude: []string{"api.pb.go"}}, false},
	} {
		b := New(Args{RepoDir: repo, FileFilter: tt.filter})
		_, units, _, err := b.DryRun(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		targetFound := false
		for _, u := range units {
			targetFound = targetFound || slices.Contains(u.Paths, "target/lib.go")
			found = found || slices.Contains(u.Paths, "api.pb.go")
		}
		if tt.want && !targetFound {
			t.Fatal("capture dropped explicitly included build output")
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
