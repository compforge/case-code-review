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
	job, err := recording.ReadJob(filepath.Join(root, repo, review+".jsonl"), unit, options...)
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] job repo=%s review=%s unit=%s: %v\n", repo, review, unit, err)
		http.Error(w, "Job observation unavailable", http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(job)
		return
	}
	tmpl, err := parseTemplate("job.html")
	if err != nil {
		http.Error(w, "template error", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err = tmpl.Execute(w, map[string]any{"Job": job, "Repo": repo, "Review": review, "Unit": unit}); err != nil {
		fmt.Fprintf(console.Err(), "[viewer] render job %s: %v\n", unit, err)
	}
}
func nodeChildren(n any) observation.Nodes {
	switch n := n.(type) {
	case *observation.Operation:
		return n.Nodes
	case *observation.Turn:
		return n.Nodes
	default:
		return nil
	}
}
func nodeDetails(n any) string {
	// Child bodies render recursively; don't repeat the entire subtree in every panel.
	switch n := n.(type) {
	case *observation.Operation:
		copy := *n
		copy.Nodes = nil
		return prettyJSON(copy)
	case *observation.Turn:
		copy := *n
		copy.Nodes = nil
		return prettyJSON(copy)
	default:
		return prettyJSON(n)
	}
}
func prettyJSON(v any) string {
	if b, ok := v.(json.RawMessage); ok {
		var decoded any
		if json.Unmarshal(b, &decoded) == nil {
			v = decoded
		}
	}
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
