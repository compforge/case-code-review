package console

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestAddErrSink(t *testing.T) {
	var sink bytes.Buffer
	restore := AddErrSink(&sink)
	fmt.Fprint(Err(), "during-sink")
	restore()

	if !strings.Contains(sink.String(), "during-sink") {
		t.Errorf("sink should capture stderr writes, got %q", sink.String())
	}
	// After restore, Err() is the original writer again — the sink sees nothing.
	fmt.Fprint(Err(), "after-restore")
	if strings.Contains(sink.String(), "after-restore") {
		t.Errorf("sink should not capture after restore, got %q", sink.String())
	}
}
