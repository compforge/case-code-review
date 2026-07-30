package runner

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyTopLevelBucketsStayRemoved(t *testing.T) {
	for _, name := range []string{"feature", "model"} {
		path := filepath.Join("..", name)
		_, err := os.Stat(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		t.Errorf("internal/%s must have an explicit owner instead of returning as a top-level bucket", name)
	}
}
