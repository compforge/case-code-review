package viewer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/compforge/agent-ledger/go/observation"
	"github.com/compforge/agentgo/codec"
	"github.com/compforge/quality-harness/sdks/go/trajectory"
	"github.com/qiankunli/case-code-review/internal/console"
)

// A bounded set of gates coalesces concurrent requests for the same Review without
// retaining per-Review state forever. Unrelated Reviews can compute concurrently.
type measurementService struct {
	runner  trajectory.Runner
	options []codec.Option
	gates   [16]chan struct{}
}

func newMeasurementService(options ...codec.Option) *measurementService {
	service := &measurementService{runner: trajectory.DefaultRunner(), options: options}
	for i := range service.gates {
		service.gates[i] = make(chan struct{}, 1)
	}
	return service
}

// +spec=`Opening a Viewer scope computes deterministic measurements on demand; identical recording and engine versions share one cached result, and changed inputs are never published`
// +link=component://docs/observability.md
func (service *measurementService) load(ctx context.Context, path string) (string, json.RawMessage, error) {
	key := sha256.Sum256([]byte(path))
	gate := service.gates[int(key[0])%len(service.gates)]
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-ctx.Done():
		return "error", nil, ctx.Err()
	}
	fingerprint, err := service.runner.Fingerprint()
	if err != nil {
		return "error", nil, err
	}
	engine := "ccr-native-session@1:" + fingerprint
	if status, views, err := readMeasurements(path, engine); err == nil && status == "ready" {
		return status, views, nil
	}
	before, err := measurementSources(path)
	if err != nil {
		return "error", nil, err
	}
	source, err := readCallSource(path)
	if err != nil {
		return "error", nil, err
	}
	catalog, err := sessionCatalog(source)
	if err != nil {
		return "error", nil, err
	}
	sessions := make([]observation.SessionView, 0, len(catalog))
	started := time.Now()
	for _, entry := range catalog {
		if err := ctx.Err(); err != nil {
			return "error", nil, err
		}
		_, view, _, err := readNativeSession(path, entry.ID, service.options...)
		if err != nil {
			return "error", nil, fmt.Errorf("measure Session %s: %w", entry.ID, err)
		}
		sessions = append(sessions, view)
	}
	run, err := service.runner.Run(ctx, sessions)
	if err != nil {
		return "error", nil, err
	}
	data, err := json.Marshal(before)
	if err != nil {
		return "error", nil, err
	}
	digest := sha256.Sum256(data)
	version := hex.EncodeToString(digest[:])
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	views, err := trajectory.Project(run, trajectory.Metadata{RunID: id + ":" + version[:12], DatasetID: id, DatasetVersion: version, CreatedAt: time.Now().UTC()})
	if err != nil {
		return "error", nil, err
	}
	raw, err := json.Marshal(views)
	if err != nil {
		return "error", nil, err
	}
	after, err := measurementSources(path)
	if err != nil {
		return "error", nil, err
	}
	if !reflect.DeepEqual(before, after) {
		return "stale", nil, nil
	}
	if err := ctx.Err(); err != nil {
		return "error", nil, err
	}
	if err := saveMeasurements(path, measurementSnapshot{Engine: engine, Sources: before, Views: raw}); err != nil {
		return "error", nil, err
	}
	fmt.Fprintf(console.Err(), "[viewer] measured review=%s sessions=%d duration=%s engine=%s\n", id, len(sessions), time.Since(started).Round(time.Millisecond), engine)
	return "ready", raw, nil
}
func saveMeasurements(path string, snapshot measurementSnapshot) error {
	target := strings.TrimSuffix(path, ".jsonl") + ".measurements.json"
	file, err := os.CreateTemp(filepath.Dir(target), ".measurements-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(snapshot)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), target)
}
