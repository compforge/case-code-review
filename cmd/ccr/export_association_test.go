package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A Lane's loops are distinct trajectories, and parallel tools may finish in
// the opposite order to their declarations. Neither ordering implies ownership.
func TestExportExecutionAndToolIdentity(t *testing.T) {
	records := []map[string]any{
		{"type": "session_start", "schema_version": 11, "sessionId": "run"},
	}
	for _, id := range []string{"e1", "e2"} {
		records = append(records,
			map[string]any{"type": "llm_request", "scope_id": "lane", "execution_id": id, "stage_id": id + "/request", "messages": []map[string]any{{"role": "user", "content": id}}},
			map[string]any{"type": "llm_response", "scope_id": "lane", "execution_id": id, "stage_id": id + "/request", "content": id, "tool_calls": []map[string]any{{"id": "a", "name": "read_files"}, {"id": "b", "name": "read_files"}}},
		)
	}
	for _, id := range []string{"e2", "e1"} {
		for _, call := range []string{"b", "a"} {
			records = append(records, map[string]any{"type": "tool_result", "scope_id": "lane", "execution_id": id, "stage_id": id + "/tool/" + call, "request_id": id + "/request", "tool_call_id": call, "result": id + call})
		}
	}
	for i, id := range []string{"e1", "e2"} {
		outcome := []string{"completed", "timeout"}[i]
		records = append(records, map[string]any{"type": "timeline_update", "timeline_id": "run", "update": map[string]any{"Stages": []map[string]any{{"id": id, "name": "execution", "revision": 2, "parent_id": "operation:run", "started_at": "2026-10-01T00:00:00Z", "finished_at": "2026-10-01T00:00:01Z", "status": "succeeded", "fields": map[string]any{"execution_id": id, "scope_id": "lane", "outcome": outcome}}}}})
	}
	file := filepath.Join(t.TempDir(), "run.jsonl")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	// A torn final append preserves earlier facts but remains observable.
	if _, err := f.WriteString(`{"type":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	trajectory, err := exportSession(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(trajectory.Subagents) != 2 || trajectory.Extra["recording_incomplete"] == nil {
		t.Fatalf("projection=%+v", trajectory)
	}
	for i, sub := range trajectory.Subagents {
		id := []string{"e1", "e2"}[i]
		if sub.TrajectoryID != id || sub.Extra["scope_id"] != "lane" || sub.Extra["execution_outcome"] != []string{"completed", "timeout"}[i] {
			t.Fatalf("execution was overwritten: %+v", sub)
		}
		if len(sub.Steps) != 2 || sub.Steps[0].Message != id {
			t.Fatalf("lost own prompt: %+v", sub.Steps)
		}
		step := sub.Steps[1]
		if _, ok := step.Metrics.Extra["duration_ms"]; ok {
			t.Fatal("missing request end became measured duration")
		}
		if len(step.Observation.Results) != 2 {
			t.Fatalf("lost tool results: %+v", step)
		}
		for _, result := range step.Observation.Results {
			if result.Content != id+result.SourceCallID {
				t.Fatalf("tool paired by arrival order: %+v", result)
			}
		}
	}
}
