package viewer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// Stage boundaries are added to the shared Session fixture without inventing
// events or changing its failed/successful attempt and cross-run identities.
func operationFixture(t *testing.T) (string, []operation.Record) {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "review.jsonl")
	source, err := readCallSource(path)
	if err != nil {
		t.Fatal(err)
	}
	stages := source.rows
	at := source.byID["execution-1"].StartedAt
	for i := range stages {
		if stages[i].ParentID == "execution-1" {
			stages[i].ParentID = "run-1/turn/1"
		}
		if stages[i].ParentID == "execution-2" {
			stages[i].ParentID = "run-2/turn/1"
		}
	}
	for i, execution := range []operation.ID{"execution-1", "execution-2"} {
		turnID := []operation.ID{"run-1/turn/1", "run-2/turn/1"}[i]
		stages = append(stages, operation.Record{ID: turnID, ParentID: execution, Name: "turn", StartedAt: at, FinishedAt: at.Add(10 * time.Second), Status: operation.Succeeded, Attributes: map[string]json.RawMessage{"turn": json.RawMessage(`1`)}})
	}
	writeOperationFixture(t, path, stages)
	return path, stages
}

func writeOperationFixture(t *testing.T, path string, stages []operation.Record) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, op := range stages {
		if err := encoder.Encode(map[string]any{"type": "operation", "review_id": "review", "operation": op}); err != nil {
			t.Fatal(err)
		}
	}
}
