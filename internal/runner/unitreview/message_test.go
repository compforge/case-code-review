package unitreview

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
)

func TestBoardOwnsReviewSemantics(t *testing.T) {
	board := msg.NewBoard("peer confirmed a caller")
	full := board.ToLLM()
	if got := full.ExtractText(); got != "peer confirmed a caller" {
		t.Fatalf("full board = %q", got)
	}
	projected, _ := board.Compact(0)
	reference := projected.(*msg.Board).ToLLM()
	if got := reference.ExtractText(); !strings.Contains(got, "peer-unit") {
		t.Fatalf("board reference = %q", got)
	}
}
