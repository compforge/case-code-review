package viewer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// +case=`A persisted generic measurement is served only for the exact Review and Session contents that produced it`
func TestMeasurementSnapshotFreshness(t *testing.T) {
	directory := t.TempDir()
	review := filepath.Join(directory, "review.jsonl")
	os.WriteFile(review, []byte("review\n"), 0600)
	os.Mkdir(filepath.Join(directory, "review.sessions"), 0700)
	session := filepath.Join(directory, "review.sessions", "lane.jsonl")
	os.WriteFile(session, []byte("session\n"), 0600)
	status, _, err := readMeasurements(review)
	if err != nil || status != "missing" {
		t.Fatalf("missing: %s %v", status, err)
	}
	hashes := map[string]string{}
	for _, path := range []string{review, session} {
		data, _ := os.ReadFile(path)
		digest := sha256.Sum256(data)
		relative, _ := filepath.Rel(directory, path)
		hashes[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
	}
	view := json.RawMessage(`{"schema":1,"dataset_id":"review","global":{"values":[{"name":"new_signal","value":2}]},"sessions":[]}`)
	encoded, _ := json.Marshal(measurementSnapshot{Sources: hashes, Views: view})
	sidecar := filepath.Join(directory, "review.measurements.json")
	os.WriteFile(sidecar, encoded, 0600)
	status, got, err := readMeasurements(review)
	if err != nil || status != "ready" || string(got) != string(view) {
		t.Fatalf("ready: %s %s %v", status, got, err)
	}
	os.WriteFile(session, []byte("session changed\n"), 0600)
	status, got, err = readMeasurements(review)
	if err != nil || status != "stale" || got != nil {
		t.Fatalf("stale: %s %s %v", status, got, err)
	}
	os.WriteFile(sidecar, []byte(`{"views":{"schema":1,"dataset_id":"other"}}`), 0600)
	status, _, err = readMeasurements(review)
	if err == nil || status != "error" {
		t.Fatalf("wrong review: %s %v", status, err)
	}
}
