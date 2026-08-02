package harness

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHarnessDoesNotImportReviewDomain(t *testing.T) {
	forbidden := []string{
		"/internal/language",
		"/internal/runner",
		"/internal/unit",
	}
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".go" {
			return err
		}
		if filepath.Base(path) == "boundary_test.go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, dependency := range forbidden {
			if strings.Contains(string(data), dependency) {
				t.Errorf("%s imports review-domain package %s", path, dependency)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgentGoDependencyStaysInsideHarness(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	harnessRoot := filepath.Join(repoRoot, "internal", "harness")
	err = filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != repoRoot && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if path == harnessRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "github.com/compforge/agentgo") {
			t.Errorf("%s imports agentgo outside internal/harness", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestLegacyLLMLoopHasNoExternalImports(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(repoRoot, "internal", "harness", "llmloop")
	legacyImport := "internal/harness/" + "llmloop"
	err = filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != repoRoot && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			if path == legacyRoot {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), legacyImport) {
			t.Errorf("%s imports the retained legacy llmloop package", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
