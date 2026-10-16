package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/compforge/go-stdx/timeline"
)

// +case:id=timeline_delta_growth,desc=`Repeated idle observations and small stage updates`,expect=`Idle observations write nothing; updates preserve native facts with bounded bytes`
func TestTimelineDeltasBoundGrowthAndPreserveFacts(t *testing.T) {
	var data bytes.Buffer
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	writer := &jsonlWriter{sessionID: "run", startTime: start, writer: bufio.NewWriter(&data)}
	snapshot := timeline.Snapshot{ID: "run", CapturedAt: start, StartedAt: start, Status: timeline.Unknown}
	for i := 0; i < 1000; i++ {
		snapshot.Stages = append(snapshot.Stages, timeline.Stage{ID: timeline.StageID(fmt.Sprintf("stage-%04d", i)), Name: "tool.execution", StartedAt: start, Status: timeline.Running})
	}
	write := func() {
		t.Helper()
		if err := writer.appendTimelineLocked(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	write()
	initialBytes := data.Len()
	for i := 0; i < 100; i++ {
		snapshot.CapturedAt = start.Add(time.Duration(i) * time.Millisecond)
		write()
	}
	if data.Len() != initialBytes {
		t.Fatal("idle polling rewrote the timeline")
	}
	// CapturedAt is observation time, not the timestamp used to order stage facts.
	for i := 0; i < 50; i++ {
		snapshot.CapturedAt = start.Add(time.Duration(i+1) * time.Second)
		snapshot.Stages[i].Status = timeline.Succeeded
		snapshot.Stages[i].FinishedAt = snapshot.CapturedAt
		write()
	}
	if data.Len() > initialBytes*2 {
		t.Fatalf("small updates rewrote history: initial=%d total=%d", initialBytes, data.Len())
	}
	transcript, err := ReadTranscript(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(transcript.Timeline, snapshot) {
		t.Fatal("delta replay changed native facts")
	}
	lines := bytes.Split(bytes.TrimSpace(data.Bytes()), []byte("\n"))
	var previous uint64
	for i, line := range lines {
		var entry timelineRecord
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if i > 0 && (entry.Type != "timeline_delta" || len(entry.Snapshot.Stages) != 1 || entry.BaseSeq != previous) {
			t.Fatalf("unexpected update: %+v", entry)
		}
		previous = entry.Seq
	}
	// A changed minute boundary and the terminal boundary remain independently readable.
	snapshot.CapturedAt = start.Add(time.Minute)
	snapshot.Stages[50].Status = timeline.Succeeded
	snapshot.Stages[50].FinishedAt = snapshot.CapturedAt
	write()
	snapshot.FinishedAt = start.Add(61 * time.Second)
	snapshot.CapturedAt = snapshot.FinishedAt
	snapshot.Status = timeline.Succeeded
	write()
	lines = bytes.Split(bytes.TrimSpace(data.Bytes()), []byte("\n"))
	for _, line := range lines[len(lines)-2:] {
		var entry timelineRecord
		if err := json.Unmarshal(line, &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Type != "timeline_snapshot" || len(entry.Snapshot.Stages) != 1000 {
			t.Fatal("missing full checkpoint")
		}
		if _, err := ReadTranscript(bytes.NewReader(line)); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("initial checkpoint=%d bytes, 50 stage updates plus checkpoint=%d bytes", initialBytes, len(bytes.Join(lines[:51], []byte("\n"))))
}

func TestTimelineFailedDeltaDoesNotAdvanceBaseline(t *testing.T) {
	var data bytes.Buffer
	writer := &jsonlWriter{sessionID: "run", writer: bufio.NewWriter(&data)}
	snapshot := timeline.Snapshot{ID: "run", CapturedAt: time.Now()}
	if err := writer.appendTimelineLocked(snapshot); err != nil {
		t.Fatal(err)
	}
	before := writer.timelineState
	sink := &failingSink{err: errors.New("disk full")}
	writer.writer = bufio.NewWriter(sink)
	snapshot.Stages = []timeline.Stage{{ID: "new", Name: "tool.execution"}}
	for i := 0; i < 2; i++ {
		if err := writer.appendTimelineLocked(snapshot); !errors.Is(err, sink.err) {
			t.Fatalf("error=%v", err)
		}
	}
	if !reflect.DeepEqual(before, writer.timelineState) || writer.sequence != 1 || sink.calls != 1 {
		t.Fatal("failed delta advanced persisted state")
	}
}

func TestTimelineSharedFixtureAndBrokenChains(t *testing.T) {
	raw, err := os.ReadFile("testdata/timeline-deltas.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	transcript, err := ReadTranscript(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(transcript.Timeline.Stages) != 2 || transcript.Timeline.Stages[0].Actor.ID != "worker-a" || transcript.Timeline.Stages[0].Status != timeline.Succeeded || transcript.Timeline.Stages[1].Actor.ID != "worker-b" || transcript.Timeline.Stages[1].Status != timeline.Failed {
		t.Fatalf("lost actor-scoped facts: %+v", transcript.Timeline)
	}
	if transcript.Timeline.Operation != "review" || transcript.Timeline.Attributes["done"] == nil || len(transcript.Records) != 2 {
		t.Fatalf("lost metadata/content: %+v", transcript)
	}
	torn := append(bytes.Clone(raw), []byte(`{"type":"timeline_delta","snapshot":`)...)
	partial, err := ReadTranscript(bytes.NewReader(torn))
	if err != nil || !partial.TruncatedTail || !reflect.DeepEqual(partial.Timeline, transcript.Timeline) {
		t.Fatalf("truncated replay: %v %+v", err, partial)
	}
	for _, tc := range []struct{ name, text string }{
		{"missing update", strings.Replace(string(raw), `"base_seq":4`, `"base_seq":3`, 1)},
		{"duplicate seq", strings.Replace(string(raw), `"seq":5`, `"seq":4`, 1)},
		{"foreign timeline", strings.Replace(string(raw), `"timeline_id":"run"`, `"timeline_id":"other"`, 1)},
		{"bad actor", strings.Replace(string(raw), `"actor_ref":1`, `"actor_ref":9`, 1)},
		{"no checkpoint", strings.Join(strings.Split(string(raw), "\n")[3:], "\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadTranscript(strings.NewReader(tc.text)); err == nil {
				t.Fatal("accepted broken timeline chain")
			}
		})
	}
}
