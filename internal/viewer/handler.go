package viewer

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
)

func handleRepos(w http.ResponseWriter, r *http.Request, root string) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	handleTimelineList(w, r, root, r.URL.Query().Get("repo"))
}

type timelineListEntry struct {
	SessionSummary
	EncodedRepo string
	RepoName    string
}

type timelineListData struct {
	Repos        []RepoInfo
	SelectedRepo string
	Timelines    []timelineListEntry
}

// +spec=`Viewer navigation starts with review timelines; repositories filter the list and local Sessions are reached through Stage details`
func handleTimelineList(w http.ResponseWriter, r *http.Request, root, selectedRepo string) {
	repos, err := DiscoverRepos(root)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := timelineListData{Repos: repos, SelectedRepo: selectedRepo}
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
			name := filepath.Base(summary.CWD)
			if name == "." || name == "" {
				name = repo.EncodedPath
			}
			data.Timelines = append(data.Timelines, timelineListEntry{SessionSummary: summary, EncodedRepo: repo.EncodedPath, RepoName: name})
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	sort.SliceStable(data.Timelines, func(i, j int) bool { return data.Timelines[i].Timestamp.After(data.Timelines[j].Timestamp) })
	renderTemplate(w, "timelines.html", data)
}

func handleSessions(w http.ResponseWriter, r *http.Request, root, repo string) {
	handleTimelineList(w, r, root, repo)
}

type sessionPageData struct {
	EncodedRepo string
	RepoName    string
	Session     *ViewSession
}

func handleSession(w http.ResponseWriter, r *http.Request, root, repo, sessionID string) {
	vs, err := LoadSession(root, repo, sessionID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to load session: %v", err), http.StatusNotFound)
		return
	}

	// Derive a display name
	name := filepath.Base(vs.Summary.CWD)
	if name == "." || name == "" {
		name = repo
	}

	renderTemplate(w, "session.html", sessionPageData{
		EncodedRepo: repo,
		RepoName:    name,
		Session:     vs,
	})
}
