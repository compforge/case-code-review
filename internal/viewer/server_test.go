package viewer

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserURL(t *testing.T) {
	tests := map[string]string{
		":3000":          "http://localhost:3000",
		"localhost:5483": "http://localhost:5483",
		"127.0.0.1:5483": "http://127.0.0.1:5483",
	}
	for addr, want := range tests {
		if got := BrowserURL(addr); got != want {
			t.Errorf("BrowserURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestTimelineHomeListsReviewsAcrossRepositories(t *testing.T) {
	root := t.TempDir()
	writeViewerSession(t, root, "repo-a", "older", `{"type":"session_start","schema_version":13,"cwd":"/src/alpha","timestamp":"2026-10-16T10:00:00Z","biz_id":"change-old"}`)
	writeViewerSession(t, root, "repo-b", "newer", `{"type":"session_start","schema_version":13,"cwd":"/src/beta","timestamp":"2026-10-16T11:00:00Z","biz_id":"change-new"}`)
	response := httptest.NewRecorder()
	handleRepos(response, httptest.NewRequest("GET", "/", nil), root)
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, `/r/repo-b/newer#timeline`) || !strings.Contains(body, `/r/repo-a/older#timeline`) {
		t.Fatalf("home=%d %s", response.Code, body)
	}
	if strings.Index(body, "change-new") > strings.Index(body, "change-old") {
		t.Fatal("timelines not ordered by start time")
	}
	if !strings.Contains(body, ">alpha</a>") || !strings.Contains(body, ">beta</a>") {
		t.Fatal("repository names lost")
	}
	response = httptest.NewRecorder()
	handleRepos(response, httptest.NewRequest("GET", "/?repo=repo-a", nil), root)
	if response.Code != 200 || strings.Contains(response.Body.String(), "change-new") || !strings.Contains(response.Body.String(), "change-old") {
		t.Fatalf("filter=%d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handleRepos(response, httptest.NewRequest("GET", "/?repo=../outside", nil), root)
	if response.Code != 404 {
		t.Fatalf("unknown repository status=%d", response.Code)
	}
}
