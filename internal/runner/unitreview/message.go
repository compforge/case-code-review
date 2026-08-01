package unitreview

import (
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// BoardDigest is a point-in-time digest of peer Unit observations. Its meaning
// belongs to Review 1; Harness only asks it for a lower-cost representation.
type BoardDigest struct {
	digest string
}

func NewBoardDigest(digest string) BoardDigest { return BoardDigest{digest: digest} }

func (b BoardDigest) ToLLM(level msg.CompactionLevel) llm.Message {
	if level >= msg.CompactionReference {
		return llm.NewTextMessage("user", "(peer-unit board notes compacted; fresh notes can be pulled again)")
	}
	return llm.NewTextMessage("user", b.digest)
}

func (b BoardDigest) MaxCompaction() msg.CompactionLevel { return msg.CompactionReference }
