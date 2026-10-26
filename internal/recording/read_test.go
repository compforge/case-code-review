package recording

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestReadNativeSessionFixture(t *testing.T) {
	transcript, err := ReadReview("../../testdata/recording/review.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if gaps := transcript.IncompleteReasons(); len(gaps) != 0 {
		t.Fatal(gaps)
	}
	costs := transcript.Costs().Total
	if costs.Calls != 3 || costs.Errors != 1 || costs.Tokens.PromptTokens != 240 || costs.Tokens.CompletionTokens != 20 {
		t.Fatalf("costs=%+v", costs)
	}
	calls := map[string]string{}
	compactions := 0
	for _, record := range transcript.Records {
		switch record["type"] {
		case "tool_result":
			calls[record["result"].(string)] = record["request_id"].(string)
		case "context_compacted":
			compactions++
			if record["tokens_after"] != float64(50) {
				t.Fatal(record)
			}
		}
	}
	if calls["evidence 1"] != "session-1/run-1/model-1/2" || calls["evidence 2"] != "session-1/run-2/model-1/1" || compactions != 2 {
		t.Fatalf("joins=%v compactions=%d", calls, compactions)
	}
}

// +case:id=session_without_timeline,desc=`timeline has not reached disk`,expect=`native IDs still join calls once and preserve tool ownership, with unattributed cost`
func TestReadSessionWithoutTimeline(t *testing.T) {
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("../../testdata/recording")); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "review.jsonl"))
	var kept []byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) > 0 && !bytes.Contains(line, []byte(`"type":"operation"`)) {
			kept = append(kept, line...)
			kept = append(kept, '\n')
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "review.jsonl"), kept, 0600); err != nil {
		t.Fatal(err)
	}
	transcript, err := ReadReview(filepath.Join(dir, "review.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	costs := transcript.Costs()
	if costs.Total.Calls != 3 || costs.Total.Errors != 1 || costs.Total.Tokens.PromptTokens != 240 || costs.Unattributed.Calls != 3 {
		t.Fatalf("costs=%+v", costs)
	}
	if len(transcript.IncompleteReasons()) == 0 {
		t.Fatal("missing timeline appeared complete")
	}
	calls := map[string]bool{}
	for _, record := range transcript.Records {
		if record["type"] == "llm_response" {
			calls[CallID(record)] = true
		}
	}
	for _, record := range transcript.Records {
		if record["type"] == "tool_result" && !calls[textField(record, "request_id")] {
			t.Fatalf("unjoined tool: %v", record)
		}
	}
}
