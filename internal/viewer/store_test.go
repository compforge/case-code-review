package viewer

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeViewerSession(t *testing.T, root, repo, sessionID string, records ...string) {
	t.Helper()
	dir := filepath.Join(root, repo)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".jsonl"), []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func sessionStart(id string) string {
	return `{"type":"session_start","sessionId":"` + id + `","schema_version":13,"timestamp":"2026-08-02T00:00:00Z","model":"test-model"}`
}

func TestDiscoverReposOnlyIncludesCurrentSchema(t *testing.T) {
	root := t.TempDir()
	repo := "example-repo"
	writeViewerSession(t, root, repo, "old", `{"type":"session_start","schema_version":5,"biz_id":"pr:old"}`)
	writeViewerSession(t, root, repo, "new", `{"type":"session_start","schema_version":13,"biz_id":"pr:new"}`)
	now := time.Now()
	oldPath := filepath.Join(root, repo, "old.jsonl")
	newPath := filepath.Join(root, repo, "new.jsonl")
	if err := os.Chtimes(oldPath, now.Add(time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newPath, now, now); err != nil {
		t.Fatal(err)
	}

	repos, err := DiscoverRepos(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].SessionCount != 1 || repos[0].LatestBizID != "pr:new" {
		t.Fatalf("repos = %+v", repos)
	}
}

func TestLoadSessionRejectsOldSchema(t *testing.T) {
	root, repo, sessionID := t.TempDir(), "example-repo", "old"
	writeViewerSession(t, root, repo, sessionID, `{"type":"session_start","schema_version":5}`)
	if _, err := LoadSession(root, repo, sessionID); err == nil || !strings.Contains(err.Error(), "unsupported session schema") {
		t.Fatalf("error = %v", err)
	}
}

func TestPeekSessionLoadsCurrentSchemaSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	transcript := strings.Join([]string{
		`{"type":"session_start","schema_version":13,"timestamp":"2026-08-02T00:00:00Z","model":"test-model"}`,
		`{"type":"session_end","duration_seconds":2,"files_reviewed":["a.go","b.go"],"diff_files":5,"diff_insertions":30,"diff_deletions":6}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := peekSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 13 || !got.HasDiffStats || got.DiffFileCount != 5 || got.FileCount != 2 {
		t.Fatalf("summary = %+v", got)
	}
}

// +case=`Timeline navigation survives corrupt Session bodies; only the selected call/Agent view reads those bodies`
func TestTimelinePageDoesNotReadNativeSessions(t *testing.T) {
	path, _ := stageAgentFixture(t)
	dir := filepath.Dir(path)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	terminal := map[string]any{"type": "session_end", "completion": map[string]any{"status": "partial", "units": []map[string]any{{"id": "u", "outcome": "timeout", "unassessed_hypotheses": []string{"h"}}}}, "tokens": map[string]int{"prompt_tokens": 123, "completion_tokens": 45}}
	if err := json.NewEncoder(file).Encode(terminal); err != nil {
		t.Fatal(err)
	}
	file.Close()
	if err := os.WriteFile(filepath.Join(dir, "review.sessions", "loop.jsonl"), []byte("corrupt body\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view, err := LoadSession(filepath.Dir(dir), filepath.Base(dir), "review")
	if err != nil {
		t.Fatal(err)
	}
	if len(view.RecordingWarnings) != 0 || view.Timeline == nil || view.Summary.Completion.Status != "partial" || view.Summary.Tokens == nil || view.Summary.Tokens.PromptTokens != 123 {
		t.Fatalf("timeline loaded body or lost business facts: %+v", view)
	}
	response := httptest.NewRecorder()
	handleSession(response, httptest.NewRequest("GET", "/", nil), filepath.Dir(dir), filepath.Base(dir), "review")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "Partial — not a clean verdict") || !strings.Contains(response.Body.String(), "123 prompt tokens") {
		t.Fatalf("timeline response: %d %s", response.Code, response.Body.String())
	}
	agent, err := loadStageAgent(path, "execution-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.Warnings) == 0 {
		t.Fatal("Agent view failed to report the corrupt evidence it actually read")
	}
}
