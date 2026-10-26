package recording

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/case-code-review/internal/recording/operation"
)

func TestTranscriptInterruptedTailAndCorruption(t *testing.T) {
	prefix := `{"type":"session_start","schema_version":14}` + "\n"
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
	if _, err := ReadTranscript(strings.NewReader(`{"type":"session_start","schema_version":11}`)); err == nil {
		t.Fatal("accepted an old schema")
	}
}

func TestTranscriptUsesLatestCompleteSnapshot(t *testing.T) {
	start := time.Now().UTC()
	stage := operation.Record{ID: "execution", ParentID: "operation:run", Name: "execution", StartedAt: start, Status: operation.Running, Attributes: map[string]json.RawMessage{"outcome": json.RawMessage(`"completed"`)}}
	var stream bytes.Buffer
	enc := json.NewEncoder(&stream)
	write := func() {
		t.Helper()

		if err := enc.Encode(map[string]any{"type": "operation", "review_id": "run", "operation": stage}); err != nil {
			t.Fatal(err)
		}
	}
	write()
	partial, err := ReadTranscript(bytes.NewReader(stream.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := partial.ExecutionFacts()[0]["outcome"]; ok {
		t.Fatal("a running stage became a completed execution")
	}
	stage.FinishedAt = start.Add(7 * time.Millisecond)
	stage.Status = operation.Succeeded
	write()
	write()
	transcript, err := ReadTranscript(&stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript.Records) != 0 {
		t.Fatal("reader retained repeated snapshot payloads")
	}
	facts := transcript.ExecutionFacts()
	if len(facts) != 1 || facts[0]["outcome"] != "completed" || facts[0]["duration_ms"] != float64(7) {
		t.Fatalf("facts=%v", facts)
	}
}

func TestTranscriptOperationIdentity(t *testing.T) {
	raw := `{"type":"operation","review_id":"run","operation":{"id":"exec","name":"execution","started_at":"2026-10-01T00:00:00Z","finished_at":"2026-10-01T00:00:01Z","status":"completed","attributes":{"outcome":"completed","turns":3}}}`
	transcript, err := ReadTranscript(strings.NewReader(raw))
	if err != nil || len(transcript.ExecutionFacts()) != 1 {
		t.Fatalf("facts=%v error=%v", transcript, err)
	}
	if _, err := ReadTranscript(strings.NewReader(raw + "\n" + strings.Replace(raw, `"review_id":"run"`, `"review_id":"other"`, 1))); err == nil {
		t.Fatal("accepted inconsistent review identity")
	}
}

type failingSink struct {
	calls int
	err   error
}

func (f *failingSink) Write(p []byte) (int, error) { f.calls++; return 0, f.err }

func TestSessionWriterSharedSequenceAndFailure(t *testing.T) {
	id := t.Name()
	defer operation.Release(id)
	if err := operation.Start(id, "review"); err != nil {
		t.Fatal(err)
	}
	defer operation.Finish(id, nil)
	var data bytes.Buffer
	writer := &jsonlWriter{sessionID: id, startTime: time.Now(), writer: bufio.NewWriter(&data)}
	writer.writeRecordLocked(map[string]any{"type": "llm_request"})
	if _, err := writer.appendLocked(map[string]any{"type": "artifact"}); err != nil {
		t.Fatal(err)
	}
	writer.writeRecordLocked(map[string]any{"type": "artifact"})
	transcript, err := ReadTranscript(&data)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[any]bool{}
	for i, record := range transcript.Records {
		if record["seq"] != float64(i+1) || record["sessionId"] != id || seen[record["uuid"]] {
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
	if _, err := writer.appendLocked(map[string]any{"type": "artifact"}); !errors.Is(err, sink.err) {
		t.Fatalf("timeline error=%v", err)
	}
	if sink.calls != 1 || writer.sequence != 3 {
		t.Fatalf("failed recording kept advancing: calls=%d seq=%d", sink.calls, writer.sequence)
	}
	if _, err := operation.Read(id); err != nil {
		t.Fatal("failed export discarded cached facts", err)
	}
}
