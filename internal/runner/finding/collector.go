package finding

import "sync"

// Collector is a thread-safe, per-Runner finding store.
type Collector struct {
	mu       sync.Mutex
	comments []Finding
}

// NewCollector creates an empty collector.
func NewCollector() *Collector {
	return &Collector{}
}

// Add appends a comment to the collector.
func (c *Collector) Add(cm Finding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.comments = append(c.comments, cm)
}

// Comments returns all collected comments.
func (c *Collector) Comments() []Finding {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Finding, len(c.comments))
	copy(out, c.comments)
	return out
}

// Snapshot returns the current count of stored comments. Pair with Since /
// ReplaceSince to operate on the comments added between two points in time
// (e.g. before / after a scan batch).
func (c *Collector) Snapshot() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.comments)
}

// Since returns a copy of all comments stored at index ≥ start. Returns nil
// when no new comments have been added since the snapshot.
func (c *Collector) Since(start int) []Finding {
	c.mu.Lock()
	defer c.mu.Unlock()
	if start < 0 {
		start = 0
	}
	if start >= len(c.comments) {
		return nil
	}
	out := make([]Finding, len(c.comments)-start)
	copy(out, c.comments[start:])
	return out
}

// ReplaceSince replaces comments[start:] with the given replacements.
// Useful for batch-level dedup: take a Snapshot, run a batch, dedup the
// new comments, then apply the deduped list back. Indices ≥ len(comments)
// are ignored (no-op).
func (c *Collector) ReplaceSince(start int, replacements []Finding) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if start < 0 {
		start = 0
	}
	if start > len(c.comments) {
		return
	}
	c.comments = append(c.comments[:start:start], replacements...)
}
