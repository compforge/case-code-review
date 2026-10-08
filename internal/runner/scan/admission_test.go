package scan

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/config/rules"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/runner/preview"
)

func TestScanAdmissionPreservesOverridesAndPreview(t *testing.T) {
	for _, git := range []bool{false, true} {
		name := "filesystem"
		if git {
			name = "git"
		}
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			if git {
				repo = initTestRepo(t)
			}
			const path = "vendor/lib/source.go"
			const body = "package lib\n"
			writeFile(t, repo, path, []byte(body))
			if git {
				gitCommit(t, repo, "dependency fixture")
			}
			for _, tc := range []struct {
				name   string
				filter *rules.FileFilter
				reason preview.ExcludeReason
			}{
				{name: "default", reason: preview.ExcludeDefaultPath},
				{name: "include", filter: &rules.FileFilter{Include: []string{"vendor/**"}}, reason: preview.ExcludeNone},
				{name: "exclude wins", filter: &rules.FileFilter{Include: []string{"vendor/**"}, Exclude: []string{"vendor/**"}}, reason: preview.ExcludeUserRule},
			} {
				t.Run(tc.name, func(t *testing.T) {
					a := New(Args{RepoDir: repo, FileFilter: tc.filter, Template: makeTemplateWithFullScan()})
					defer a.session.Finalize()
					items, err := NewProvider(repo, nil, nil, 0).Enumerate(t.Context(), a.selectPath)
					if err != nil || len(items) != 1 {
						t.Fatalf("enumeration: items=%v err=%v", items, err)
					}
					want := ""
					if tc.reason == preview.ExcludeNone {
						want = body
					}
					if items[0].Content != want {
						t.Fatalf("content=%q, want %q", items[0].Content, want)
					}
					view, err := a.Preview(t.Context())
					if err != nil || len(view.Entries) != 1 {
						t.Fatalf("preview: view=%+v err=%v", view, err)
					}
					entry := view.Entries[0]
					if entry.Path != path || entry.ExcludeReason != tc.reason || entry.WillReview != (tc.reason == preview.ExcludeNone) {
						t.Fatalf("unexpected preview: %+v", entry)
					}
				})
			}
		})
	}
}

func TestScanExcludedFilesNeedNoReadPermission(t *testing.T) {
	repo := t.TempDir()
	const path = "vendor/unreadable.go"
	writeFile(t, repo, path, []byte("package vendor\n"))
	full := filepath.Join(repo, path)
	if err := os.Chmod(full, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(full, 0o644) })
	if f, err := os.Open(full); err == nil {
		f.Close()
		t.Skip("environment can read files without read permission")
	}
	var warnings bytes.Buffer
	defer console.AddErrSink(&warnings)()
	a := New(Args{RepoDir: repo, Template: makeTemplateWithFullScan()})
	defer a.session.Finalize()
	view, err := a.Preview(t.Context())
	if err != nil || len(view.Entries) != 1 {
		t.Fatalf("excluded path must remain visible without reading it: %+v, %v", view, err)
	}
	if view.Entries[0].ExcludeReason != preview.ExcludeDefaultPath || warnings.Len() != 0 {
		t.Fatalf("unexpected exclusion or content I/O: %+v, warnings=%s", view, warnings.String())
	}
}

func TestScanRunDiffMapContainsOnlyAdmittedBodies(t *testing.T) {
	repo := initTestRepo(t)
	writeFile(t, repo, "main.go", []byte("package main\n"))
	writeFile(t, repo, "vendor/dependency.go", []byte("package dependency\n"))
	writeFile(t, repo, "large.go", []byte(strings.Repeat("var extra = 12345\n", 3000)))
	gitCommit(t, repo, "scan fixture")
	registry := tool.NewRegistry()
	diffs := tool.NewFileReadDiff(tool.DiffMap{})
	registry.Register(diffs)
	a := New(Args{RepoDir: repo, Template: makeTemplateWithFullScan(), Tools: registry,
		LLMClient: &fakeBudgetClient{perCallTokens: 1}, MaxConcurrency: 1})
	if _, err := a.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(a.items) != 1 || a.items[0].Path != "main.go" {
		t.Fatalf("unexpected admitted files: %+v", a.items)
	}
	for _, path := range []string{"main.go", "vendor/dependency.go", "large.go"} {
		body, err := diffs.Execute(t.Context(), map[string]any{"paths": []any{path}})
		if err != nil {
			t.Fatal(err)
		}
		if path == "main.go" {
			if !strings.Contains(body, "package main") {
				t.Fatalf("missing admitted body: %s", body)
			}
		} else if body != "Error: diff not found for the requested paths" {
			t.Fatalf("excluded body retained for %s", path)
		}
	}
}
