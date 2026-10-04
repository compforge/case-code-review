package viewer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConcurrentRequestsAssociateByStageIdentity(t *testing.T) {
	root, repo, id := t.TempDir(), "repo", "run"
	writeViewerSession(t, root, repo, id, sessionStart(id),
		`{"type":"llm_request","scope_id":"unit","kind":"unit","execution_id":"exec","taskType":"main_task","stage_id":"r1","request_no":1}`,
		`{"type":"llm_request","scope_id":"unit","kind":"unit","execution_id":"exec","taskType":"main_task","stage_id":"r2","request_no":2}`,
		`{"type":"llm_response","scope_id":"unit","kind":"unit","execution_id":"exec","taskType":"main_task","stage_id":"r2","content":"second"}`,
		`{"type":"llm_response","scope_id":"unit","kind":"unit","execution_id":"exec","taskType":"main_task","stage_id":"r1","content":"first","tool_calls":[{"id":"a","name":"read_files"},{"id":"b","name":"read_files"}]}`,
		`{"type":"tool_result","request_id":"r1","tool_call_id":"b","result":"B","ok":true}`,
		`{"type":"tool_result","request_id":"r1","tool_call_id":"a","result":"A","ok":true}`,
	)
	view, err := LoadSession(root, repo, id)
	if err != nil {
		t.Fatal(err)
	}
	calls := view.Reviews[0].Calls
	if len(calls) != 2 || calls[0].ResponseContent != "first" || calls[1].ResponseContent != "second" {
		t.Fatalf("requests misassociated: %+v", calls)
	}
	if calls[0].ToolCalls[0].Result != "A" || calls[0].ToolCalls[1].Result != "B" {
		t.Fatalf("parallel tool results misassociated: %+v", calls[0].ToolCalls)
	}
}

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
	if view.RecordingWarning == "" || len(view.Reviews) != 1 {
		t.Fatalf("lost intact prefix or warning: %+v", view)
	}
}
