package timelinelog

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline/model"
	"github.com/compforge/go-stdx/timeline/store"
)

// +case:id=incremental_timeline_storage,desc=`hundreds of stages finish in sequence`,expect=`each append contains only changed stages and repeated flushes add no bytes`
func TestIncrementalStorageReplayAndTornTail(t *testing.T) {
	backend := New()
	path := filepath.Join(t.TempDir(), "review.timeline")
	if err := backend.Attach("review", path); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	at := time.Now().UTC()
	start := store.Update{Operation: &store.OperationRecord{Revision: 1, Operation: "review", StartedAt: at, Status: model.Unknown}}
	if err := backend.Merge(ctx, "review", start); err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		stage := model.Stage{ID: model.StageID(string(rune('a' + i))), Name: "unit", StartedAt: at, FinishedAt: at.Add(time.Second), Status: model.Succeeded}
		update := store.Update{Stages: []store.StageUpdate{{Stage: stage, Revision: 1}}}
		if err := backend.Merge(ctx, "review", update); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Stat(path)
		if err := backend.Merge(ctx, "review", update); err != nil {
			t.Fatal(err)
		}
		again, _ := os.Stat(path)
		if again.Size() != info.Size() {
			t.Fatal("duplicate update appended")
		}
	}
	if err := backend.Detach("review"); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.Split(bytes.TrimSpace(raw), []byte("\n"))) != 301 {
		t.Fatal("unexpected update count")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
		var e entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatal(err)
		}
		if len(e.Update.Stages) > 1 {
			t.Fatal("append contains historical stages")
		}
	}
	doc, _, torn, err := Read(bytes.NewReader(append(raw, []byte(`{"seq":`)...)))
	if err != nil || !torn || len(doc.Stages) != 300 {
		t.Fatalf("replay=%d torn=%v err=%v", len(doc.Stages), torn, err)
	}
	if _, _, _, err := Read(bytes.NewReader(append(raw, []byte("{\n")...))); err == nil {
		t.Fatal("accepted complete corruption")
	}
	if _, err := backend.Read(ctx, "review"); err != store.ErrNotFound {
		t.Fatalf("closed review retained memory: %v", err)
	}
}
