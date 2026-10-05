package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
)

func TestTranscriptInterruptedTailAndCorruption(t *testing.T) {
	prefix := `{"type":"session_start","schema_version":11}` + "\n"
	for _, tc := range []struct {
		name, tail     string
		truncated, bad bool
	}{
		{"torn JSON", `{"type":`, true, false},
		{"torn UTF8", string([]byte{'"', 0xe4}), true, false},
		{"complete without newline", `{"type":"finding"}`, false, false},
		{"corrupt complete line", "{\n", false, true},
		{"non-object", "null\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transcript, err := ReadTranscript(strings.NewReader(prefix + tc.tail))
			if (err != nil) != tc.bad {
				t.Fatalf("error=%v", err)
			}
			if err == nil && (transcript.TruncatedTail != tc.truncated || len(transcript.Records) < 1) {
				t.Fatalf("transcript=%+v", transcript)
			}
		})
	}
	if _, err := ReadTranscript(strings.NewReader(`{"type":"session_start","schema_version":10}`)); err == nil {
		t.Fatal("accepted an old schema")
	}
}

func TestTranscriptNativeStageRevisions(t *testing.T) {
	start := time.Now().UTC()
	stage := timeline.Stage{ID: "execution", ParentID: "operation:run", Name: "execution", StartedAt: start, Status: timeline.Running, Attributes: map[string]json.RawMessage{"outcome": json.RawMessage(`"completed"`)}}
	var stream bytes.Buffer
	enc := json.NewEncoder(&stream)
	write := func(revision uint64) {
		t.Helper()
		if err := enc.Encode(map[string]any{"type": "timeline_update", "timeline_id": "run", "update": timeline.Update{Stages: []timeline.StageUpdate{{Stage: stage, Revision: revision}}}}); err != nil {
			t.Fatal(err)
		}
	}
	write(1)
	partial, err := ReadTranscript(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := partial.ExecutionFacts()[0]["outcome"]; ok {
		t.Fatal("a running stage became a completed execution")
	}
	stage.FinishedAt = start.Add(7 * time.Millisecond)
	stage.Status = timeline.Succeeded
	write(2)
	write(2)
	transcript, err := ReadTranscript(&stream)
	if err != nil {
		t.Fatal(err)
	}
	facts := transcript.ExecutionFacts()
	if len(facts) != 1 || facts[0]["outcome"] != "completed" || facts[0]["duration_ms"] != float64(7) {
		t.Fatalf("facts=%v", facts)
	}
}

func TestTranscriptReadsAttributesAndLegacyFields(t *testing.T) {
	for _, key := range []string{"attributes", "fields"} {
		t.Run(key, func(t *testing.T) {
			raw := `{"type":"timeline_update","timeline_id":"run","update":{"Stages":[{"revision":2,"id":"exec","name":"execution","started_at":"2026-10-01T00:00:00Z","finished_at":"2026-10-01T00:00:01Z","status":"succeeded","` + key + `":{"execution_id":"exec","outcome":"completed","turns":3}}]}}`
			transcript, err := ReadTranscript(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			facts := transcript.ExecutionFacts()
			if len(facts) != 1 || facts[0]["outcome"] != "completed" || facts[0]["turns"] != float64(3) || facts[0]["duration_ms"] != float64(1000) {
				t.Fatalf("lost execution facts: %v", facts)
			}
			if transcript.Timeline.Stages[0].Revision != 2 {
				t.Fatal("lost stage revision")
			}
		})
	}
}

type failingSink struct {
	calls int
	err   error
}

func (f *failingSink) Write(p []byte) (int, error) { f.calls++; return 0, f.err }

func TestSessionWriterSharedSequenceAndFailure(t *testing.T) {
	var data bytes.Buffer
	writer := &jsonlWriter{sessionID: "run", startTime: time.Now(), writer: bufio.NewWriter(&data)}
	writer.writeRecordLocked(map[string]any{"type": "llm_request"})
	if err := writer.writeTimeline("run", timeline.Update{}); err != nil {
		t.Fatal(err)
	}
	writer.writeRecordLocked(map[string]any{"type": "artifact"})
	transcript, err := ReadTranscript(&data)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[any]bool{}
	for i, record := range transcript.Records {
		if record["seq"] != float64(i+1) || record["sessionId"] != "run" || seen[record["uuid"]] {
			t.Fatalf("envelope=%v", record)
		}
		if _, ok := record["parentUuid"]; ok {
			t.Fatal("storage adjacency was recorded as parentage")
		}
		seen[record["uuid"]] = true
	}
	sink := &failingSink{err: errors.New("disk full")}
	writer.writer = bufio.NewWriter(sink)
	if _, err := writer.appendLocked(map[string]any{"type": "artifact"}); !errors.Is(err, sink.err) {
		t.Fatalf("write error=%v", err)
	}
	if err := writer.writeTimeline("run", timeline.Update{}); !errors.Is(err, sink.err) {
		t.Fatalf("timeline error=%v", err)
	}
	if sink.calls != 1 || writer.sequence != 3 {
		t.Fatalf("failed recording kept advancing: calls=%d seq=%d", sink.calls, writer.sequence)
	}
}
