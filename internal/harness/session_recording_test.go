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
	session := &LoopSession{}
	defer session.Close()
	spec := ExecutionSpec{LoopSession: session, LLMClient: client, Messages: []agentgo.AgentMessage{msg.Text("user", "private prompt body")}, Session: review, Scope: recording.Scope{ID: "lane-1", Kind: "lane"}, NaturalCompletion: true, MaxTurns: 1}
	first, err := runExecution(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	spec.Messages = []agentgo.AgentMessage{msg.Text("user", "next hypothesis")}
	second, err := runExecution(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if session.path == "" || first.ID == second.ID || first.State != OutcomeCompleted || second.State != OutcomeCompleted {
		t.Fatal("continuation changed Session")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
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
	f, err := os.Open(session.path)
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
	if err := os.Remove(session.path); err != nil {
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

// +case:id=lane_checkpoint_reload,desc=`a Lane finishes a run and its session is reopened from disk`,expect=`a new execution appends input to the loaded messages and artifacts with fresh limits and per-run usage`
func TestLoopSessionCheckpointRunsWithoutResidentAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	review := recording.New(t.TempDir(), "main", "model", recording.SessionOptions{})
	defer review.Finalize()
	session := &LoopSession{}
	client := &scriptedClient{responses: []*llm.ChatResponse{
		toolCallResponseID("done", "task_done", `{}`, &llm.UsageInfo{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}),
		toolCallResponseID("done", "task_done", `{}`, &llm.UsageInfo{PromptTokens: 20, CompletionTokens: 3, TotalTokens: 23}),
	}}
	spec := ExecutionSpec{LoopSession: session, Session: review, LLMClient: client,
		Scope: recording.Scope{ID: "lane", Kind: "lane"}, MaxTurns: 1,
		Messages: []agentgo.AgentMessage{msg.Text("user", "hyp-first"), msg.NewFile("a.go", 1, 1, 1, "1|retained source")},
		ToolDefs: []llm.ToolDef{toolDef("task_done")},
	}
	first, err := runExecution(t.Context(), spec)
	if err != nil || first.State != OutcomeCompleted || first.Turns != 1 || first.Usage.TotalTokens != 12 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	id := session.local.ID()
	if len(session.state.Tools) != 0 {
		t.Fatal("retained execution-bound tool closures")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := msg.Codec()
	if err != nil {
		t.Fatal(err)
	}
	local, err := agentsession.Open(session.path, c)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, ok, err := local.LoadCheckpoint()
	if err != nil || !ok {
		local.Close()
		t.Fatalf("checkpoint exists=%v err=%v", ok, err)
	}
	if snapshot.State.Progress.Active || snapshot.State.Progress.CompletedTurns != 1 || len(snapshot.State.Artifacts) != 1 {
		local.Close()
		t.Fatalf("checkpoint lost committed state: %+v", snapshot.State)
	}
	restored := &LoopSession{state: snapshot.State, local: local, path: session.path}
	defer restored.Close()
	spec.LoopSession = restored
	spec.Messages = []agentgo.AgentMessage{msg.Text("user", "hyp-second")}
	second, err := runExecution(t.Context(), spec)
	if err != nil || second.State != OutcomeCompleted || second.Turns != 1 || second.Usage.TotalTokens != 23 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if restored.local.ID() != id || len(restored.state.Artifacts) != 1 {
		t.Fatal("reload changed session identity or lost material")
	}
	text := requestText(client.Requests()[1])
	if !strings.Contains(text, "hyp-first") || !strings.Contains(text, "hyp-second") || !strings.Contains(text, "retained source") {
		t.Fatal("reloaded request lost prior messages")
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(session.path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	runs, checkpoints := 0, 0
	_, err = agentsession.Read(f, func(r agentsession.Record) error {
		if r.Type == agentsession.RunStart {
			runs++
		}
		if r.Type == agentsession.Checkpoint {
			checkpoints++
		}
		return nil
	})
	if err != nil || runs != 2 || checkpoints != 2 {
		t.Fatalf("runs=%d checkpoints=%d err=%v", runs, checkpoints, err)
	}
}
