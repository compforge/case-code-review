package viewer

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/agent-ledger/go/common"
	"github.com/compforge/agent-ledger/go/observation"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// +case=`A continuous Lane exposes both native runs and retry attempts once, without embedding request bodies in the Session summary`
func TestCompleteNativeSession(t *testing.T) {
	entry, view, source, err := readNativeSession("../../testdata/recording/review.jsonl", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	page := summarizeSession(entry, view, source)
	runs := map[string]bool{}
	calls := map[string]sessionItem{}
	for _, item := range page.Items {
		if item.Kind == "model_call" || item.Kind == "tool_call" {
			runs[item.Ref.RunID] = true
			if _, ok := calls[item.ID]; ok {
				t.Fatal("duplicate native call", item.ID)
			}
			calls[item.ID] = item
		}
	}
	if len(runs) != 2 || len(entry.Executions) != 2 || entry.Role != "R2 Lane" {
		t.Fatalf("split Lane: %+v runs=%v", entry, runs)
	}
	foundFailed, foundSuccess := false, false
	for _, item := range calls {
		if item.Kind == "model_call" && item.Ref.RunID == "run-1" {
			if item.Ref.Attempt == 1 {
				foundFailed = item.Status == "failed" && item.Error == "temporary provider failure" && item.Usage == nil
			}
			if item.Ref.Attempt == 2 {
				foundSuccess = item.Status == "completed" && item.Usage != nil && *item.Usage.InputTokens == 100 && item.Previous != ""
			}
		}
	}
	if !foundFailed || !foundSuccess {
		t.Fatalf("lost independent attempts: %+v", calls)
	}
	linked := false
	for _, item := range calls {
		if item.Kind == "tool_call" && item.Ref.RunID == "run-1" && item.ModelCallID != "" {
			linked = true
		}
	}
	if !linked {
		t.Fatal("recorded tool call lost requesting model association")
	}
	raw, _ := json.Marshal(page)
	for _, forbidden := range []string{"inspect caller", "check the guard", "model_request", "\"request\""} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("summary leaked body", forbidden)
		}
	}
}

// +case=`Unfinished calls keep unknown durations and outcomes; evidence gaps do not manufacture a successful zero-duration result`
func TestSessionSummaryUnknownDuration(t *testing.T) {
	page := summarizeSession(nativeSession{ID: "s"}, observation.SessionView{Nodes: observation.Nodes{
		&observation.ModelCall{Node: observation.Node{ID: "unfinished", StartedAt: "2026-11-04T00:00:00Z"}},
		&observation.ModelCall{Node: observation.Node{ID: "missing-start", FinishedAt: "2026-11-04T00:00:01Z", Status: "failed"}},
	}}, &callSource{byID: map[operation.ID]operation.Record{}})
	for _, item := range page.Items {
		if item.DurationMS != nil || item.Usage != nil {
			t.Fatalf("invented observation: %+v", item)
		}
	}
	if page.Items[0].Status != "unknown" || page.Items[1].Status != "failed" {
		t.Fatal(page.Items)
	}
}

func TestNativeSessionEndpointsAndMissingEvidence(t *testing.T) {
	root := t.TempDir()
	repo := "repo"
	dir := filepath.Join(root, repo)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	query := `/session?session=session-1&node=call:%5B%22run-1%22,%22model_call%22,%22model-1%22,2%5D`
	res := httptest.NewRecorder()
	handleNativeSession(res, httptest.NewRequest("GET", query, nil), root, repo, "review")
	if res.Code != 200 || !strings.Contains(res.Body.String(), "inspect caller") || strings.Contains(res.Body.String(), "record:4") {
		t.Fatalf("detail %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	handleNativeSession(res, httptest.NewRequest("GET", query, nil), root, "../repo", "review")
	if res.Code != 400 {
		t.Fatal(res.Code)
	}
	res = httptest.NewRecorder()
	handleNativeSession(res, httptest.NewRequest("GET", "/session?session=unassociated", nil), root, repo, "review")
	if res.Code != 404 {
		t.Fatal(res.Code)
	}
	if err := os.Remove(filepath.Join(dir, "review.sessions", "loop.jsonl")); err != nil {
		t.Fatal(err)
	}
	res = httptest.NewRecorder()
	handleNativeSession(res, httptest.NewRequest("GET", "/session?session=session-1", nil), root, repo, "review")
	var page sessionPage
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &page) != nil || len(page.Issues) == 0 || len(page.Items) != 0 {
		t.Fatal(res.Body.String())
	}
}

func TestSearchBatchOutcomes(t *testing.T) {
	output, _ := json.Marshal(map[string]any{"content": tool.EncodeCodeSearchResults([]string{"File: a.go\nHit line: 1", `Search outcome: {"status":"no_matches","searched_files":2}` + "\nNo matches", `Search outcome: {"status":"scope_empty","searched_files":0}` + "\nNo files", "Error: capture unavailable"})})
	call := &observation.ToolCall{Request: &common.CallRequest{ToolName: "search_code", Input: json.RawMessage(`{"searches":[{"query":"hit"},{"query":"empty"},{"query":"wrong scope"},{"query":"failure"},{"query":"missing"}]}`)}, Response: &common.CallResponse{Output: output}}
	result := searchResults(call)
	for i, want := range []string{"hits", "no_matches", "scope_empty", "error", "incomplete"} {
		if result[i].Status != want {
			t.Fatalf("query %d got %s want %s", i, result[i].Status, want)
		}
	}
}

// +case=`Call bodies selected by native ID preserve reasoning, tools, usage and failures without borrowing another attempt or Run`
func TestNativeCallBodies(t *testing.T) {
	root := t.TempDir()
	repo := "repo"
	if err := os.MkdirAll(filepath.Join(root, repo), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(filepath.Join(root, repo), os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	read := func(run, kind string, attempt int) map[string]any {
		t.Helper()
		idBytes, _ := json.Marshal([]any{run, kind, "model-1", attempt})
		if kind == "tool_call" {
			idBytes, _ = json.Marshal([]any{run, kind, "same-call", attempt})
		}
		query := "/session?session=session-1&node=" + url.QueryEscape("call:"+string(idBytes))
		res := httptest.NewRecorder()
		handleNativeSession(res, httptest.NewRequest("GET", query, nil), root, repo, "review")
		var body map[string]any
		if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &body) != nil {
			t.Fatalf("detail: %d %s", res.Code, res.Body.String())
		}
		return body["node"].(map[string]any)
	}
	failed := read("run-1", "model_call", 1)
	raw, _ := json.Marshal(failed)
	if !strings.Contains(string(raw), "temporary provider failure") || strings.Contains(string(raw), "read evidence") {
		t.Fatalf("failed call: %s", raw)
	}
	success := read("run-1", "model_call", 2)
	raw, _ = json.Marshal(success)
	for _, want := range []string{"inspect caller", "read evidence", "check the guard", "read_files", "input_tokens"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("lost %s: %s", want, raw)
		}
	}
	tool := read("run-1", "tool_call", 1)
	if tool["tool_call_id"] != "same-call" || tool["response"] == nil {
		t.Fatal(tool)
	}
	second := read("run-2", "model_call", 1)
	if second["run_id"] != "run-2" || second["response"] == nil {
		t.Fatal(second)
	}
}
