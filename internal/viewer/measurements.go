package viewer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/qiankunli/case-code-review/internal/console"
)

type measurementSnapshot struct {
	Sources map[string]string `json:"sources"`
	Views   json.RawMessage   `json:"views"`
}

// +spec=`The Viewer reads persisted Harness measurements only; changed recordings invalidate the snapshot without running analysis`
// +link=component://docs/observability.md
func readMeasurements(path string) (string, json.RawMessage, error) {
	data, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".measurements.json")
	if os.IsNotExist(err) {
		return "missing", nil, nil
	}
	if err != nil {
		return "error", nil, err
	}
	var snapshot measurementSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return "error", nil, err
	}
	var header struct {
		Schema    int    `json:"schema"`
		DatasetID string `json:"dataset_id"`
	}
	if err := json.Unmarshal(snapshot.Views, &header); err != nil {
		return "error", nil, err
	}
	if header.Schema != 1 || header.DatasetID != strings.TrimSuffix(filepath.Base(path), ".jsonl") {
		return "error", nil, fmt.Errorf("measurement snapshot schema or Review identity mismatch")
	}
	files, err := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + ".sessions/*.jsonl")
	if err != nil {
		return "error", nil, err
	}
	digests := map[string]string{}
	for _, file := range append(files, path) {
		f, err := os.Open(file)
		if err != nil {
			return "error", nil, err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		f.Close()
		if err != nil {
			return "error", nil, err
		}
		relative, err := filepath.Rel(filepath.Dir(path), file)
		if err != nil {
			return "error", nil, err
		}
		digests[filepath.ToSlash(relative)] = hex.EncodeToString(hash.Sum(nil))
	}
	if !reflect.DeepEqual(digests, snapshot.Sources) {
		return "stale", nil, nil
	}
	return "ready", snapshot.Views, nil
}

func handleMeasurements(w http.ResponseWriter, r *http.Request, root, repo, review string) {
	if repo == "" || review == "" || strings.Contains(repo+review, "..") || strings.ContainsAny(repo+review, `/\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	status, views, err := readMeasurements(filepath.Join(root, repo, review+".jsonl"))
	if err != nil {
		fmt.Fprintf(console.Err(), "[viewer] measurements repo=%s review=%s: %v\n", repo, review, err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(struct {
		Status string          `json:"status"`
		Views  json.RawMessage `json:"views,omitempty"`
	}{status, views}); err != nil {
		fmt.Fprintf(console.Err(), "[viewer] measurements response review=%s: %v\n", review, err)
	}
}
