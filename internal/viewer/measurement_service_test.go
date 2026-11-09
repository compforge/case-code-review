package viewer

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/compforge/agent-ledger/go/observation"
	"github.com/compforge/quality-harness/sdks/go/trajectory"
)

type countedMeasurer struct {
	calls   *atomic.Int32
	version string
	mutate  func()
}

func (m countedMeasurer) Spec() trajectory.MeasurerSpec {
	return trajectory.MeasurerSpec{ID: "new_signal", Version: m.version, Title: "New signal", Category: "cost", Measurements: []trajectory.MeasurementSpec{{Name: "occurrences", Unit: "count", Aggregations: []string{"sum"}}}}
}
func (m countedMeasurer) Measure(_ context.Context, _ observation.SessionView) (trajectory.MeasurementResult, error) {
	m.calls.Add(1)
	if m.mutate != nil {
		m.mutate()
	}
	return trajectory.MeasurementResult{MeasurerID: "new_signal", Status: "measured", Measurements: map[string]float64{"occurrences": 2}}, nil
}
func measurementFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS("../../testdata/trajectory")); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "review.jsonl")
}

// +case=`Concurrent Viewer requests compute each native Session once, reuse disk results across service restarts, and invalidate on recording or Measurer version changes`
// +link=component://docs/observability.md
func TestAutomaticMeasurementCache(t *testing.T) {
	path := measurementFixture(t)
	service := newMeasurementService()
	var calls atomic.Int32
	service.runner.Measurers = append(service.runner.Measurers, countedMeasurer{calls: &calls, version: "1"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, raw, err := service.load(context.Background(), path)
			if err != nil || status != "ready" {
				t.Errorf("load: %s %v", status, err)
				return
			}
			var views trajectory.Views
			if err := json.Unmarshal(raw, &views); err != nil {
				t.Error(err)
			}
			if len(views.Sessions) != 2 {
				t.Errorf("sessions: %d", len(views.Sessions))
			}
			found := false
			for _, row := range views.Global.Values {
				if row.MeasurerID == "new_signal" {
					found = true
					if row.Value == nil || *row.Value != 4 {
						t.Error("custom signal not aggregated")
					}
				}
			}
			if !found {
				t.Error("custom signal not rendered")
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("measured %d times, want 2", calls.Load())
	}
	restarted := newMeasurementService()
	restarted.runner = service.runner
	if status, _, err := restarted.load(context.Background(), path); err != nil || status != "ready" {
		t.Fatal(status, err)
	}
	if calls.Load() != 2 {
		t.Fatal("disk cache was not reused")
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	file.WriteString("\n")
	file.Close()
	if status, _, err := service.load(context.Background(), path); err != nil || status != "ready" {
		t.Fatal(status, err)
	}
	if calls.Load() != 4 {
		t.Fatal("recording change did not invalidate cache")
	}
	service.runner.Measurers[len(service.runner.Measurers)-1] = countedMeasurer{calls: &calls, version: "2"}
	if status, _, err := service.load(context.Background(), path); err != nil || status != "ready" {
		t.Fatal(status, err)
	}
	if calls.Load() != 6 {
		t.Fatal("Measurer version change did not invalidate cache")
	}
}

func TestChangedRecordingDoesNotPublishMeasurements(t *testing.T) {
	path := measurementFixture(t)
	service := newMeasurementService()
	var calls atomic.Int32
	service.runner.Measurers = []trajectory.Measurer{countedMeasurer{calls: &calls, version: "1", mutate: func() {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		file.WriteString("\n")
	}}}
	status, raw, err := service.load(context.Background(), path)
	if err != nil || status != "stale" || raw != nil {
		t.Fatal(status, err)
	}
	if _, err := os.Stat(strings.TrimSuffix(path, ".jsonl") + ".measurements.json"); !os.IsNotExist(err) {
		t.Fatal("inconsistent snapshot was saved", err)
	}
}
func TestCanceledMeasurementWait(t *testing.T) {
	service := newMeasurementService()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status, _, err := service.load(ctx, filepath.Join(t.TempDir(), "missing.jsonl"))
	if status != "error" || err == nil {
		t.Fatal(status, err)
	}
}

// +case=`The HTTP measurement endpoint produces results for a new recording without Python or a pre-generated sidecar`
func TestMeasurementEndpointGeneratesSnapshot(t *testing.T) {
	path := measurementFixture(t)
	service := newMeasurementService()
	response := httptest.NewRecorder()
	service.handle(response, httptest.NewRequest("GET", "/measurements", nil), filepath.Dir(filepath.Dir(path)), filepath.Base(filepath.Dir(path)), "review")
	var result struct {
		Status string           `json:"status"`
		Views  trajectory.Views `json:"views"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || result.Status != "ready" {
		t.Fatalf("response %d: %s", response.Code, response.Body.String())
	}
	for _, row := range result.Views.Global.Values {
		if row.MeasurerID == "tool_usage" && row.Name == "tool_call_count" && (row.Value == nil || *row.Value != 3) {
			t.Fatalf("tool total: %+v", row)
		}
		if row.MeasurerID == "model_usage" && row.Name == "model_call_count" && (row.Value == nil || *row.Value != 5) {
			t.Fatalf("model total: %+v", row)
		}
	}
}
