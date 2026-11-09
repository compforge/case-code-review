package viewer

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"

	"github.com/qiankunli/case-code-review/internal/console"
)

func handleRepos(w http.ResponseWriter, r *http.Request, root string) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	handleReviewList(w, r, root, r.URL.Query().Get("repo"))
}

type reviewListEntry struct {
	SessionSummary
	EncodedRepo string
	RepoName    string
}

type reviewListData struct {
	Repos        []RepoInfo
	Date         string
	Status       string
	SelectedRepo string
	Reviews      []reviewListEntry
}

// +spec=`Global summarizes one Review; Job and Session provide scoped views; Context describes a selected model call`
// +link=component://docs/observability.md
func handleReviewList(w http.ResponseWriter, r *http.Request, root, selectedRepo string) {
	repos, err := DiscoverRepos(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := reviewListData{Repos: repos, SelectedRepo: selectedRepo, Date: r.URL.Query().Get("date"), Status: r.URL.Query().Get("status")}
	found := selectedRepo == ""
	for _, repo := range repos {
		if selectedRepo != "" && repo.EncodedPath != selectedRepo {
			continue
		}
		found = true
		summaries, err := ListSessions(root, repo.EncodedPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, summary := range summaries {
			if data.Date != "" && summary.Timestamp.In(cstZone).Format("2006-01-02") != data.Date {
				continue
			}
			if data.Status != "" && summary.Completion.Status != data.Status {
				continue
			}
			name := filepath.Base(summary.CWD)
			if name == "." || name == "" {
				name = repo.EncodedPath
			}
			data.Reviews = append(data.Reviews, reviewListEntry{SessionSummary: summary, EncodedRepo: repo.EncodedPath, RepoName: name})
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	sort.SliceStable(data.Reviews, func(i, j int) bool { return data.Reviews[i].Timestamp.After(data.Reviews[j].Timestamp) })
	renderTemplate(w, "reviews.html", data)
}

func handleSessions(w http.ResponseWriter, r *http.Request, root, repo string) {
	handleReviewList(w, r, root, repo)
}

type sessionPageData struct {
	EncodedRepo string
	RepoName    string
	Session     *ViewSession
}

func handleSession(w http.ResponseWriter, r *http.Request, root, repo, sessionID string) {
	vs, err := LoadSession(root, repo, sessionID)
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] review repo=%s review=%s: %v\n", repo, sessionID, err)
		http.Error(w, "Review unavailable", http.StatusNotFound)
		return
	}

	// Derive a display name
	name := filepath.Base(vs.Summary.CWD)
	if name == "." || name == "" {
		name = repo
	}

	renderTemplate(w, "review.html", sessionPageData{
		EncodedRepo: repo,
		RepoName:    name,
		Session:     vs,
	})
}
