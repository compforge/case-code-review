package unitreview

import (
	"strings"
	"testing"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
)

func TestBoardOwnsReviewSemantics(t *testing.T) {
	board := msg.NewBoard("peer confirmed a caller")
	full, _ := board.ToMessage()
	if got := full.TextContent(); got != "peer confirmed a caller" {
		t.Fatalf("full board = %q", got)
	}
	projected, _ := board.Compact(0)
	reference, _ := projected.(*msg.Board).ToMessage()
	if got := reference.TextContent(); !strings.Contains(got, "peer-unit") {
		t.Fatalf("board reference = %q", got)
	}
}
