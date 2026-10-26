package recording

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

func TestCostsKeepFailedAndUnfinishedCallsAndUnionChildTime(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	stage := func(id, parent string, a, b int) operation.Record {
		s := operation.Record{ID: operation.ID(id), ParentID: operation.ID(parent), Name: id, StartedAt: start.Add(time.Duration(a) * time.Second), Status: operation.Running}
		if b > 0 {
			s.FinishedAt = start.Add(time.Duration(b) * time.Second)
			s.Status = operation.Succeeded
		}
		return s
	}

	records := []map[string]any{
		{"type": "session_start", "schema_version": SchemaVersion},
		{"type": "llm_request", "operation_id": "req1"},
		{"type": "llm_response", "operation_id": "req1", "usage_source": "reported", "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 20, "cache_read_tokens": 30}},
		{"type": "llm_error", "operation_id": "req2"},
		{"type": "llm_request", "operation_id": "req3", "timestamp": start.Add(10 * time.Second).Format(time.RFC3339)},
		{"type": "debrief", "tokens": map[string]int{"prompt_tokens": 9999}},
	}
	var data bytes.Buffer
	for _, r := range records {
		if err := json.NewEncoder(&data).Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	transcript, err := ReadTranscript(&data)
	if err != nil {
		t.Fatal(err)
	}
	transcript.Journal = operation.Snapshot{ID: "cost", Operations: []operation.Record{stage("execution", "operation:cost", 0, 10), stage("req1", "execution", 1, 7), stage("req2", "execution", 3, 9), stage("req3", "execution", 9, 0)}}

	report := transcript.Costs()
	if report.Total.Calls != 3 || report.Total.Errors != 1 || report.Total.Pending != 1 || report.Total.UnknownUsage != 2 || report.Total.Tokens.PromptTokens != 100 {
		t.Fatalf("cost=%+v", report)
	}
	for _, s := range report.Operations {
		if s.Operation.ID == "execution" && (s.Cost != report.Total || s.DurationMS != 10000 || s.SelfMS != 1000) {
			t.Fatalf("parent cost/time: %+v", s)
		}
		if s.Operation.ID == "req3" && (s.Finished || s.DurationMS != 1000) {
			t.Fatalf("running stage lost lower bound: %+v", s)
		}
	}
	if len(transcript.IncompleteReasons()) != 2 {
		t.Fatalf("gaps=%v", transcript.IncompleteReasons())
	}
}
