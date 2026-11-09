package viewer

import (
	"context"
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
	"time"

	"github.com/qiankunli/case-code-review/internal/console"
)

type measurementSnapshot struct {
	Engine  string            `json:"engine,omitempty"`
	Sources map[string]string `json:"sources"`
	Views   json.RawMessage   `json:"views"`
}

// +spec=`Persisted measurements are reused only when recording contents and the measurement engine version match`
// +link=component://docs/observability.md
func readMeasurements(path string, engine ...string) (string, json.RawMessage, error) {
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
	if len(engine) > 0 && snapshot.Engine != engine[0] {
		return "stale", nil, nil
	}
	digests, err := measurementSources(path)
	if err != nil {
		return "error", nil, err
	}
	if !reflect.DeepEqual(digests, snapshot.Sources) {
		return "stale", nil, nil
	}
	return "ready", snapshot.Views, nil
}

func measurementSources(path string) (map[string]string, error) {
	files, err := filepath.Glob(strings.TrimSuffix(path, ".jsonl") + ".sessions/*.jsonl")
	if err != nil {
		return nil, err
	}
	digests := map[string]string{}
	for _, file := range append(files, path) {
		f, err := os.Open(file)
		if err != nil {
			return nil, err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, f)
		f.Close()
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(filepath.Dir(path), file)
		if err != nil {
			return nil, err
		}
		digests[filepath.ToSlash(relative)] = hex.EncodeToString(hash.Sum(nil))
	}
	return digests, nil
}

func (service *measurementService) handle(w http.ResponseWriter, r *http.Request, root, repo, review string) {
	if repo == "" || review == "" || strings.Contains(repo+review, "..") || strings.ContainsAny(repo+review, `/\`) {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	status, views, err := service.load(ctx, filepath.Join(root, repo, review+".jsonl"))
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
