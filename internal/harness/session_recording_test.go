package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/agentgo"
	agentsession "github.com/compforge/agentgo/session"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
)

// +case:id=lane_session_continuation,desc=`two sequential executions retain one Lane context`,expect=`one native Session contains distinct runs, while timeline references disambiguate repeated call IDs`
func TestContinuationReusesNativeSessionAndReferences(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	review := recording.New(t.TempDir(), "main", "model", recording.SessionOptions{})
	defer review.Finalize()
	client := &scriptedClient{responses: []*llm.ChatResponse{textResponse("first answer"), textResponse("second answer")}}
	spec := ExecutionSpec{LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "private prompt body")}, Session: review, Scope: recording.Scope{ID: "lane-1", Kind: "lane"}, NaturalCompletion: true, MaxTurns: 1}
	first, err := runExecution(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.ContinueFrom = &first
	spec.Messages = []agentgo.AgentMessage{msg.Text("user", "next hypothesis")}
	second, err := runExecution(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if first.sessionPath == "" || first.sessionPath != second.sessionPath {
		t.Fatal("continuation changed Session")
	}
	review.Finalize()
	path, _ := review.TranscriptPath()
	transcript, err := recording.ReadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript.IncompleteReasons()) != 0 {
		t.Fatal(transcript.IncompleteReasons())
	}
	calls := map[string]bool{}
	sessions := map[string]bool{}
	runs := map[string]bool{}
	for _, r := range transcript.Records {
		if r["type"] != "llm_request" {
			continue
		}
		ref := r["session_ref"].(map[string]any)
		sessions[ref["session_id"].(string)] = true
		runs[ref["run_id"].(string)] = true
		calls[r["stage_id"].(string)] = true
	}
	if len(sessions) != 1 || len(runs) != 2 || len(calls) != 2 {
		t.Fatalf("sessions=%v runs=%v calls=%v", sessions, runs, calls)
	}
	f, err := os.Open(first.sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, err = agentsession.Read(f, func(r agentsession.Record) error {
		var data map[string]any
		_ = json.Unmarshal(r.Data, &data)
		if data["scope_id"] != nil || data["task_type"] != nil {
			t.Fatal("business schema entered framework Session")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".timeline")
	if strings.Contains(string(raw), "private prompt body") || strings.Contains(string(raw), "first answer") {
		t.Fatal("body duplicated into timeline")
	}
	files, _ := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + ".sessions/*.jsonl")
	if len(files) != 1 {
		t.Fatalf("local files=%v", files)
	}
	if err := os.Remove(first.sessionPath); err != nil {
		t.Fatal(err)
	}
	missing, err := recording.ReadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing.Timeline.Stages) == 0 || len(missing.IncompleteReasons()) == 0 {
		t.Fatal("missing Session hid timeline or appeared complete")
	}
}

// +case:id=session_capture_failure,desc=`local Session directory cannot be created`,expect=`business execution still drains to completion and the review reports a recording gap`
func TestSessionCaptureFailureDoesNotAbortExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	review := recording.New(t.TempDir(), "main", "model", recording.SessionOptions{})
	defer review.Finalize()
	path, _ := review.TranscriptPath()
	if err := os.WriteFile(strings.TrimSuffix(path, ".jsonl")+".sessions", []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{responses: []*llm.ChatResponse{textResponse("answer")}}
	result, err := runExecution(context.Background(), ExecutionSpec{LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "review")}, Session: review, Scope: recording.Scope{ID: "unit-1", Kind: "unit"}, NaturalCompletion: true, MaxTurns: 1})
	if err != nil || result.State != OutcomeCompleted || result.Turns != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	review.Finalize()
	transcript, err := recording.ReadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript.Timeline.Stages) == 0 || len(transcript.IncompleteReasons()) == 0 {
		t.Fatal("capture failure hid timeline or appeared complete")
	}
}

// +case:id=session_codec_failure,desc=`a caller forgets to register its domain message codec`,expect=`recording reports a gap while the loop still completes its model and tool work`
func TestSessionCodecFailureDoesNotAbortExecution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	review := recording.New(t.TempDir(), "main", "model", recording.SessionOptions{})
	defer review.Finalize()
	client := &scriptedClient{responses: []*llm.ChatResponse{toolCallResponse("task_done", `{}`, nil)}}
	result, err := runExecution(context.Background(), ExecutionSpec{LLMClient: client, Messages: []agentgo.AgentMessage{newMaterialTestMessage("file-1", "snapshot", "source", "source body")}, ToolDefs: []llm.ToolDef{toolDef("task_done")}, Session: review, Scope: recording.Scope{ID: "unit-1", Kind: "unit"}, MaxTurns: 1})
	if err != nil || result.State != OutcomeCompleted || result.Turns != 1 || result.ToolCalls != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	review.Finalize()
	path, _ := review.TranscriptPath()
	transcript, err := recording.ReadReview(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range transcript.Records {
		if record["artifact_kind"] == "recording_gap" {
			found = true
		}
	}
	if !found || len(transcript.IncompleteReasons()) == 0 {
		t.Fatal("codec failure appeared complete")
	}
}
