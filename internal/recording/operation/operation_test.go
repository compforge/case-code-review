package operation

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// +case=`Concurrent operations append once per transition; cancellation records a terminal state and Release evicts runtime history`
func TestConcurrentRecordingAndRelease(t *testing.T) {
	id := t.Name()
	defer Release(id)
	var records []Record
	if err := Start(id, "review", func(r Record) error { records = append(records, r); return nil }); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Go(func() {
			h, err := Begin(id, "work", WithAttributes(Attribute{Key: "unit_id", Value: "u"}))
			if err != nil {
				t.Error(err)
				return
			}
			if err := h.End(context.Canceled); err != nil {
				t.Error(err)
			}
			if err := h.End(nil); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if err := Finish(id, nil); err != nil {
		t.Fatal(err)
	}
	if len(records) != 62 {
		t.Fatalf("updates=%d", len(records))
	}
	snapshot, err := Read(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Operations) != 30 {
		t.Fatal(len(snapshot.Operations))
	}
	for _, op := range snapshot.Operations {
		if op.Status != Canceled {
			t.Fatal(op)
		}
	}
	snapshot.Operations[0].Attributes["unit_id"] = json.RawMessage(`"changed"`)
	again, _ := Read(id)
	if string(again.Operations[0].Attributes["unit_id"]) != `"u"` {
		t.Fatal("snapshot mutated recorder")
	}
	Release(id)
	if _, err := Read(id); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
func TestWriteFailureStickyAndDuplicateIdentity(t *testing.T) {
	id := t.Name()
	defer Release(id)
	diskFull := errors.New("disk full")
	writes := 0
	if err := Start(id, "review", func(Record) error {
		writes++
		if writes == 3 {
			return diskFull
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	h, err := Begin(id, "work", WithID("work"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Begin(id, "work", WithID("work")); err == nil {
		t.Fatal("duplicate operation accepted")
	}
	if err := h.End(nil); !errors.Is(err, diskFull) {
		t.Fatal(err)
	}
	if err := Finish(id, nil); !errors.Is(err, diskFull) {
		t.Fatal(err)
	}
	if writes != 3 {
		t.Fatalf("appended after broken record: %d", writes)
	}
}
