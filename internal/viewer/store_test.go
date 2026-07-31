package viewer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSessionKeepsAgentcorePromptsAndReviewArtifacts(t *testing.T) {
	root := t.TempDir()
	repo := "example-repo"
	sessionID := "session-1"
	dir := filepath.Join(root, repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	transcript := strings.Join([]string{
		`{"type":"session_start","sessionId":"session-1","model":"test-model"}`,
		`{"type":"llm_request","scope_id":"unit-1","kind":"unit","scope":"file","paths":["a.go"],"filePath":"a.go","taskType":"main_task","request_no":1,"messages":[{"role":"system","content":"investigate"},{"role":"user","content":"review a.go"}]}`,
		`{"type":"artifact","artifact_kind":"review_hypothesis","data":{"id":"h-1","path":"a.go"}}`,
		`{"type":"llm_request","scope_id":"hypothesis_review:case-1","kind":"run","scope":"hypothesis_review","paths":["a.go"],"filePath":"a.go","taskType":"hypothesis_review_task","request_no":1,"messages":[{"role":"system","content":"verify evidence"},{"role":"user","content":"assess h-1"}]}`,
		`{"type":"artifact","artifact_kind":"review_assessment","data":{"hypothesis_id":"h-1","passed_trial":false}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadSession(root, repo, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SystemPrompts) != 2 {
		t.Fatalf("system prompts = %d, want 2", len(got.SystemPrompts))
	}
	if len(got.Units) != 2 || got.Units[1].Kind != "run" {
		t.Fatalf("scopes = %+v, want unit plus run-level review", got.Units)
	}
	if len(got.Artifacts) != 2 || !strings.Contains(got.Artifacts[1].Data, `"passed_trial": false`) {
		t.Fatalf("artifacts = %+v", got.Artifacts)
	}
}
