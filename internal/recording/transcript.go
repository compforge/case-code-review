package recording

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

// Transcript keeps append-only content and the native stage projection together.
// A missing stage is missing evidence; consumers must not infer timings from
// adjacent JSONL entries. Runtime handles never cross the recording boundary.
type Transcript struct {
	Records       []map[string]any
	Journal       operation.Snapshot
	TruncatedTail bool
	Gaps          []string
}

// ReadTranscript accepts a valid prefix followed by a torn final line. A malformed
// complete line is corruption, not an event to silently skip. Both Viewer and
// export use this contract so an interrupted run has the same evidence in each.
func ReadTranscript(r io.Reader) (*Transcript, error) {
	t := &Transcript{}
	reader := bufio.NewReader(r)
	reviewID := ""
	for lineNo := 1; ; lineNo++ {
		line, err := reader.ReadBytes('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if len(bytes.TrimSpace(line)) > 0 {
			var record map[string]any
			decodeErr := json.Unmarshal(line, &record)
			if !utf8.Valid(line) || record == nil {
				decodeErr = fmt.Errorf("invalid record encoding")
			}
			if decodeErr != nil {
				if err == io.EOF {
					t.TruncatedTail = true
					break
				}
				return nil, fmt.Errorf("session line %d: %w", lineNo, decodeErr)
			}
			if record["type"] == "session_start" && record["schema_version"] != float64(SchemaVersion) {
				return nil, fmt.Errorf("unsupported session schema %v; requires %d", record["schema_version"], SchemaVersion)
			}
			if record["type"] == "session_start" {
				reviewID, _ = record["sessionId"].(string)
			}
			if record["type"] == "operation" {
				raw, _ := json.Marshal(record["operation"])
				var op operation.Record
				if e := json.Unmarshal(raw, &op); e != nil || op.ID == "" {
					return nil, fmt.Errorf("review line %d: invalid operation", lineNo)
				}
				id, _ := record["review_id"].(string)
				if id == "" || (reviewID != "" && reviewID != id) || (t.Journal.ID != "" && t.Journal.ID != id) {
					return nil, fmt.Errorf("review line %d: inconsistent review ID", lineNo)
				}
				t.Journal.Apply(id, op)
			} else {
				t.Records = append(t.Records, record)
			}
		}
		if err == io.EOF {
			break
		}
	}
	return t, nil
}

// OperationIndex is a read-only association index for content records and stages.
func (t *Transcript) OperationIndex() map[operation.ID]operation.Record {
	stages := make(map[operation.ID]operation.Record, len(t.Journal.Operations))
	for _, stage := range t.Journal.Operations {
		stages[stage.ID] = stage
	}
	return stages
}

// ExecutionFacts projects only execution stages. Outcome is caller-owned data;
// it is never guessed from generic stage success or child completion.
func (t *Transcript) ExecutionFacts() []map[string]any {
	var records []map[string]any
	for _, stage := range t.Journal.Operations {
		if stage.Name != "execution" {
			continue
		}
		record := map[string]any{}
		for key, raw := range stage.Attributes {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				continue
			}
			record[key] = value
		}
		record["operation_id"] = string(stage.ID)
		record["taskType"] = record["task_type"]
		if !stage.FinishedAt.IsZero() {
			record["duration_ms"] = float64(stage.Duration(time.Time{}).Milliseconds())
		} else {
			delete(record, "outcome")
		}
		records = append(records, record)
	}
	return records
}
