package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/compforge/agent-ledger/go/observation"
	"github.com/compforge/agentgo/codec"
	"github.com/qiankunli/case-code-review/internal/console"
	"github.com/qiankunli/case-code-review/internal/recording"
)

func handleJob(w http.ResponseWriter, r *http.Request, root, repo, review string, options ...codec.Option) {
	if repo == "" || review == "" || strings.ContainsAny(repo, "/\\") || strings.Contains(repo, "..") || strings.ContainsAny(review, "/\\") || strings.Contains(review, "..") {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	unit := r.URL.Query().Get("unit")
	if unit == "" {
		http.Error(w, "unit is required", http.StatusBadRequest)
		return
	}
	path := filepath.Join(root, repo, review+".jsonl")
	var job observation.JobView
	var err error
	if r.URL.Query().Get("format") == "json" {
		job, err = recording.ReadJob(path, unit, options...)
	} else {
		job, err = recording.ReadJobSummary(path, unit)
	}
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] job repo=%s review=%s unit=%s: %v\n", repo, review, unit, err)
		http.Error(w, "Job observation unavailable", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(job); err != nil {
		fmt.Fprintf(console.Err(), "[viewer] job response unit=%s: %v\n", unit, err)
	}
}
