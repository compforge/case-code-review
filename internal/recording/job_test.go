package recording

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/agent-ledger/go/observation"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

func jobFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "review.jsonl")
	tr, err := ReadJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	enc := json.NewEncoder(file)
	for _, op := range tr.Journal.Operations {
		if op.Name != "execution" {
			continue
		}
		unit := "u1"
		if op.ID == "execution-2" {
			unit = "u2"
		}
		op.Attributes["unit_id"], _ = json.Marshal(unit)
		if err := enc.Encode(map[string]any{"type": "operation", "review_id": "review", "operation": op}); err != nil {
			t.Fatal(err)
		}
	}
	for _, unit := range []string{"u1", "u2"} {
		if err := enc.Encode(map[string]any{"type": "artifact", "artifact_kind": "review_unit", "data": map[string]any{"unit_id": unit, "paths": []string{unit + ".go"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := enc.Encode(map[string]any{"type": "session_end", "completion": Completion{Status: "partial", Units: []UnitResult{{ID: "u1", Outcome: "completed"}, {ID: "u2", Outcome: "timeout", Unassessed: []string{"h2"}}}}}); err != nil {
		t.Fatal(err)
	}
	return path
}
func callNodes(nodes observation.Nodes) observation.Nodes {
	var out observation.Nodes
	for _, n := range nodes {
		switch n := n.(type) {
		case *observation.ModelCall, *observation.ToolCall:
			out = append(out, n)
		case *observation.Operation:
			out = append(out, callNodes(n.Nodes)...)
		case *observation.Turn:
			out = append(out, callNodes(n.Nodes)...)
		}
	}
	return out
}

// +case=`One continuous R2 Session serves two Units; each Job selects only its own run and all failed/successful attempts, preserving native identity`
func TestUnitJobSelectsSharedSessionWithoutCrossUnitCalls(t *testing.T) {
	path := jobFixture(t)
	first, err := ReadJob(path, "u1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReadJob(path, "u2")
	if err != nil {
		t.Fatal(err)
	}
	if first.SchemaVersion != observation.JobSchemaVersion || first.Status != "completed" || second.Status != "failed" {
		t.Fatalf("status: %s %s", first.Status, second.Status)
	}
	calls := callNodes(first.Nodes)
	if len(calls) != 3 {
		t.Fatalf("first calls=%v issues=%v", calls, first.Issues)
	}
	for _, n := range calls {
		if n.Base().Session == nil || n.Base().Session.ID != "session-1" || n.Base().RunID != "run-1" {
			t.Fatalf("foreign call %+v", n.Base())
		}
	}
	calls = callNodes(second.Nodes)
	if len(calls) != 2 || calls[0].Base().RunID != "run-2" {
		t.Fatalf("second calls=%v", calls)
	}
	if first.Job.ID == second.Job.ID || !strings.Contains(string(first.Goal), "u1.go") {
		t.Fatalf("job identity or goal: %+v", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	// An unrelated corrupt Session must not be read by the Job query.
	if err := os.WriteFile(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", "unrelated.jsonl"), []byte("invalid\n"), 0600); err != nil {
		t.Fatal(err)
	}
	again, err := ReadJob(path, "u1")
	if err != nil || len(again.Issues) != len(first.Issues) {
		t.Fatalf("unrelated evidence affected job: %v %+v", err, again.Issues)
	}
}
func TestUnitJobRetainsMissingEvidenceAndBusinessUnknown(t *testing.T) {
	path := jobFixture(t)
	if err := os.Remove(filepath.Join(strings.TrimSuffix(path, ".jsonl")+".sessions", "loop.jsonl")); err != nil {
		t.Fatal(err)
	}
	job, err := ReadJob(path, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" || len(job.Nodes) == 0 || len(job.Issues) == 0 || len(callNodes(job.Nodes)) != 0 {
		t.Fatalf("missing evidence: %+v", job)
	}
	// No business receipt means no clean verdict, even when every operation ended.
	raw, _ := os.ReadFile(path)
	lines := strings.Split(string(raw), "\n")
	var kept []string
	for _, line := range lines {
		if !strings.Contains(line, `"type":"session_end"`) {
			kept = append(kept, line)
		}
	}
	if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0600); err != nil {
		t.Fatal(err)
	}
	job, err = ReadJob(path, "u1")
	if err != nil || job.Status != "running" {
		t.Fatalf("completion inferred: %s %v", job.Status, err)
	}
}
func TestJobInventoryIncludesAdmittedUnitsWithoutExecution(t *testing.T) {
	tr := &Transcript{Journal: operation.Snapshot{ID: "review"}, Records: []map[string]any{{"type": "artifact", "artifact_kind": "review_unit", "data": map[string]any{"unit_id": "u", "paths": []string{"a.go"}}}}}
	jobs := tr.Jobs()
	if len(jobs) != 1 || jobs[0].ID != "u" || jobs[0].Outcome != "running" {
		t.Fatal(jobs)
	}
}
