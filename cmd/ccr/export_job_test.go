package main

import (
	"encoding/json"
	"github.com/compforge/agent-ledger/go/observation"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// +case=`Offline Job export preserves Unit identity and recording gaps without assigning a quality verdict`
func TestExportUnitJob(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.jsonl")
	body := `{"type":"session_start","sessionId":"review","schema_version":14}
{"type":"artifact","artifact_kind":"review_unit","data":{"unit_id":"u1","paths":["a.go"]}}
{"type":"artifact","artifact_kind":"recording_gap","data":{"error":"checkpoint failed"}}
`
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	old := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = old }()
	exportErr := runExport([]string{path, "--format", "job", "--unit", "u1"})
	writer.Close()
	os.Stdout = old
	if exportErr != nil {
		t.Fatal(exportErr)
	}
	raw, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var job observation.JobView
	if err := json.Unmarshal(raw, &job); err != nil {
		t.Fatal(err)
	}
	if job.Job.ID != "review/u1" || job.Status != "running" || len(job.Issues) == 0 {
		t.Fatalf("lost identity, business state or gaps: %+v", job)
	}
	if err := runExport([]string{path, "--format", "job", "--unit", "missing"}); err == nil {
		t.Fatal("missing Unit exported as a valid job")
	}
}

func TestJobExportRequiresExplicitUnit(t *testing.T) {
	for _, args := range [][]string{{"--format", "job"}, {"--unit", "u1"}, {"--unit"}} {
		if err := runExport(args); err == nil {
			t.Fatalf("accepted ambiguous selection: %v", args)
		}
	}
}
