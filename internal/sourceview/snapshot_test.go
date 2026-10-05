package sourceview

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestCapturedMissingSourceNeverBecomesEmptyOrBaseline(t *testing.T) {
	view := New(t.TempDir(), "", "review", map[string]File{
		"missing.go": {Unavailable: true}, "deleted.go": {Deleted: true}, "new.go": {Content: "package p\nfunc Present() {}\n"},
	}, nil)
	if _, err := view.Read(context.Background(), "missing.go"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("capture failure became absence: %v", err)
	}
	if _, err := view.Read(context.Background(), "deleted.go"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("deleted entry: %v", err)
	}
	if _, err := view.Read(context.Background(), "../outside"); err == nil {
		t.Fatal("accepted outside path")
	}
	out, detail, err := view.Grep(context.Background(), []string{"--no-pager", "grep", "-n", "-F", "-e", "Present", "--"}, "")
	if err == nil || !strings.Contains(out, "Present") || !strings.Contains(detail, "unavailable") {
		t.Fatalf("partial search lost evidence/gap: %q %q %v", out, detail, err)
	}
}
