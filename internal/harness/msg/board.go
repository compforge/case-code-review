package msg

import "github.com/qiankunli/case-code-review/internal/llm"

// Board is the digest of peer units' bulletins injected at a turn boundary
// (docs/cross-unit.md). Like File it is re-derivable — the content came from
// the board and can be pulled again — so it participates in eviction: under
// token pressure a stale board digest is shed before the model's own
// reasoning, the same re-derivability ordering File uses.
//
// It is NOT dedup-eligible (each digest is a distinct point-in-time snapshot,
// not a re-read of the same range), so it implements only the evict path.
type Board struct {
	digest  string
	evicted bool
}

// NewBoard wraps a rendered board digest as an evictable user message.
func NewBoard(digest string) *Board { return &Board{digest: digest} }

func (b *Board) Lower() llm.Message {
	if b.evicted {
		return llm.NewTextMessage("user",
			"(peer-unit board notes elided to fit the context budget)")
	}
	return llm.NewTextMessage("user", b.digest)
}

// Reclaim elides the digest under token pressure (idempotent; msg.Reclaimable).
// Board notes are the most re-derivable slice of the conversation — losing them
// costs nothing the loop can't re-pull — so eviction sheds them first.
func (b *Board) Reclaim() { b.evicted = true }

// Reclaimed reports whether the digest has been elided.
func (b *Board) Reclaimed() bool { return b.evicted }
