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
