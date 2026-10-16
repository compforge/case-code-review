package session

import (
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/go-stdx/timeline/model"
)

const timelineCheckpointInterval = time.Minute

type timelineRecord struct {
	Type       string            `json:"type"`
	TimelineID string            `json:"timeline_id"`
	Seq        uint64            `json:"seq"`
	BaseSeq    uint64            `json:"base_seq"`
	Snapshot   timeline.Snapshot `json:"snapshot"`
}

// timelineRecording reconstructs native facts without retaining checkpoint copies.
// +spec=`Deltas replace whole stages by native Stage ID and Actor identity, and must reference the previous timeline record; metadata is a complete replacement.`
// +why=`An interrupted prefix must expose missing evidence rather than silently joining a delta to the wrong checkpoint.`
// +link=component://docs/observability.md
type timelineRecording struct {
	metadata timeline.Snapshot
	stages   map[model.StageKey]timeline.Stage
	sequence uint64
}

func (r *timelineRecording) apply(entry timelineRecord) error {
	snapshot := entry.Snapshot
	if entry.TimelineID == "" || snapshot.ID != entry.TimelineID || (r.metadata.ID != "" && snapshot.ID != r.metadata.ID) {
		return fmt.Errorf("inconsistent timeline ID")
	}
	if entry.Type == "timeline_delta" && (r.sequence == 0 || entry.BaseSeq != r.sequence || entry.Seq <= entry.BaseSeq) {
		return fmt.Errorf("timeline delta sequence gap: base=%d previous=%d seq=%d", entry.BaseSeq, r.sequence, entry.Seq)
	}
	if entry.Type == "timeline_snapshot" {
		r.stages = make(map[model.StageKey]timeline.Stage, len(snapshot.Stages))
	}
	for _, stage := range snapshot.Stages {
		r.stages[stage.Key()] = stage
	}
	snapshot.Stages = nil
	r.metadata, r.sequence = snapshot, entry.Seq
	return nil
}

func (r *timelineRecording) snapshot() timeline.Snapshot {
	snapshot := r.metadata
	for _, stage := range r.stages {
		snapshot.Stages = append(snapshot.Stages, stage)
	}
	sort.Slice(snapshot.Stages, func(i, j int) bool {
		a, b := snapshot.Stages[i], snapshot.Stages[j]
		if !a.StartedAt.Equal(b.StartedAt) {
			return a.StartedAt.Before(b.StartedAt)
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Actor.Key() < b.Actor.Key()
	})
	return snapshot
}

// appendTimelineLocked commits the comparison baseline only after a successful write.
// +spec=`Unchanged observations write nothing. Initial, final and minute checkpoints are complete; other writes contain only changed stages and current metadata.`
// +why=`Recording grows with actual work and state changes, not the number of idle polling ticks.`
func (jw *jsonlWriter) appendTimelineLocked(snapshot timeline.Snapshot) error {
	if jw.closed {
		return fmt.Errorf("session %s is closed", jw.sessionID)
	}
	if jw.writeErr != nil {
		return jw.writeErr
	}
	previous := &jw.timelineState
	full := previous.sequence == 0 || !snapshot.FinishedAt.IsZero() || snapshot.CapturedAt.Sub(jw.timelineCheckpoint) >= timelineCheckpointInterval
	current := make(map[model.StageKey]timeline.Stage, len(snapshot.Stages))
	var changed []timeline.Stage
	for _, stage := range snapshot.Stages {
		key := stage.Key()
		current[key] = stage
		if old, ok := previous.stages[key]; !ok || !reflect.DeepEqual(old, stage) {
			changed = append(changed, stage)
		}
	}
	// A complete checkpoint also represents a removed stage without a second
	// deletion protocol. Ordinary recording only adds or updates stages.
	if !full {
		for key := range previous.stages {
			if _, ok := current[key]; !ok {
				full = true
				break
			}
		}
	}
	metadata := snapshot
	metadata.Stages = nil
	before := previous.metadata
	before.CapturedAt, metadata.CapturedAt = time.Time{}, time.Time{}
	if previous.sequence != 0 && len(current) == len(previous.stages) && len(changed) == 0 && reflect.DeepEqual(before, metadata) {
		return nil
	}
	payload := snapshot
	record := map[string]any{"type": "timeline_snapshot", "timeline_id": snapshot.ID}
	if !full {
		payload.Stages = changed
		record["type"], record["base_seq"] = "timeline_delta", previous.sequence
	}
	record["snapshot"] = payload
	if _, err := jw.appendLocked(record); err != nil {
		return err
	}
	snapshot.Stages = nil
	*previous = timelineRecording{metadata: snapshot, stages: current, sequence: jw.sequence}
	if full {
		jw.timelineCheckpoint = snapshot.CapturedAt
	}
	return nil
}
