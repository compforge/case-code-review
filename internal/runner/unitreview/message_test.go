package unitreview

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
)

func TestBoardDigestOwnsReviewSemantics(t *testing.T) {
	board := NewBoardDigest("peer confirmed a caller")
	full := board.ToLLM(msg.CompactionNone)
	if got := full.ExtractText(); got != "peer confirmed a caller" {
		t.Fatalf("full board = %q", got)
	}
	reference := board.ToLLM(msg.CompactionReference)
	if got := reference.ExtractText(); !strings.Contains(got, "peer-unit") {
		t.Fatalf("board reference = %q", got)
	}
}
