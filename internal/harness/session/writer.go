package session

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/compforge/go-stdx/timeline"
	"github.com/compforge/go-stdx/uuid"
	"github.com/qiankunli/case-code-review/internal/console"
)

// appendLocked owns the envelope and visibility boundary for every record,
// including timeline snapshots. Sequence is storage order, not causality.
func (jw *jsonlWriter) appendLocked(rec map[string]any) (string, error) {
	if jw.closed {
		return "", fmt.Errorf("session %s is closed", jw.sessionID)
	}
	if jw.writeErr != nil {
		return "", jw.writeErr
	}
	id := uuid.V4()
	rec["uuid"], rec["seq"] = id, jw.sequence+1
	rec["sessionId"] = jw.sessionID
	rec["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	rec["elapsed_ms"] = jw.elapsedMilliseconds()
	data, err := json.Marshal(rec)
	if err == nil {
		_, err = jw.writer.Write(append(data, '\n'))
	}
	if err == nil {
		err = jw.writer.Flush()
	}
	if err != nil {
		jw.writeErr = err
		fmt.Fprintf(console.Err(), "[ccr session] recording failed session=%s seq=%d type=%v: %v\n", jw.sessionID, jw.sequence+1, rec["type"], err)
		return "", err
	}
	jw.sequence++
	return id, nil
}

func (jw *jsonlWriter) writeRecordLocked(rec map[string]any) string {
	id, _ := jw.appendLocked(rec)
	return id
}

func addStageFields(rec map[string]any, timelineID string, stageID timeline.StageID) {
	if stageID != "" {
		rec["timeline_id"], rec["stage_id"] = timelineID, stageID
	}
}
