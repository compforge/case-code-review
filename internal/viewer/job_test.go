package viewer

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/compforge/agent-ledger/go/observation"
)

func TestJobPageAndCanonicalJSON(t *testing.T) {
	root := t.TempDir()
	writeViewerSession(t, root, "repo", "review", sessionStart("review"),
		`{"type":"operation","review_id":"review","operation":{"id":"operation:review","name":"review","started_at":"2026-10-01T00:00:00Z","status":"running"}}`,
		`{"type":"artifact","artifact_kind":"review_unit","data":{"unit_id":"u1","paths":["a.go"],"instruction":"<script>alert(1)</script>"}}`,
		`{"type":"debrief","kind":"unit","scope_id":"u1","outcome":"completed"}`)
	page := httptest.NewRecorder()
	handleJob(page, httptest.NewRequest("GET", "/job?unit=u1", nil), root, "repo", "review")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Unit Job") || strings.Contains(page.Body.String(), "<script>alert(1)</script>") {
		t.Fatalf("page: %d %s", page.Code, page.Body.String())
	}
	response := httptest.NewRecorder()
	handleJob(response, httptest.NewRequest("GET", "/job?unit=u1&format=json", nil), root, "repo", "review")
	var job observation.JobView
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &job) != nil || job.Job.ID != "review/u1" || job.Status != "completed" {
		t.Fatalf("JSON: %d %s", response.Code, response.Body.String())
	}
	if err := job.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{"../repo", "a/b", `a\b`} {
		result := httptest.NewRecorder()
		handleJob(result, httptest.NewRequest("GET", "/job?unit=u1", nil), root, repo, "review")
		if result.Code != 400 {
			t.Fatal(repo, result.Code)
		}
	}
	missing := httptest.NewRecorder()
	handleJob(missing, httptest.NewRequest("GET", "/job?unit=unknown", nil), root, "repo", "review")
	if missing.Code != 404 || strings.Contains(missing.Body.String(), root) {
		t.Fatal(missing.Body.String())
	}
}
