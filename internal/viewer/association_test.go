package viewer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestViewerReportsTruncatedTail(t *testing.T) {
	root, repo, id := t.TempDir(), "repo", "run"
	writeViewerSession(t, root, repo, id, sessionStart(id), `{"type":"llm_request","scope_id":"unit","kind":"unit","stage_id":"r1"}`)
	path := filepath.Join(root, repo, id+".jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"type":`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := LoadSession(root, repo, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RecordingWarnings) == 0 {
		t.Fatalf("lost intact prefix or warning: %+v", view)
	}
}
