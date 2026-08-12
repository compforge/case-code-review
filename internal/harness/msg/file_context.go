package msg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/llm"
)

// FileContextView describes how much navigation context was supplied for a
// statically related file. Exact source is represented only by File.
type FileContextView string

const (
	// ViewSource is emitted by File context items; FileContext itself carries
	// only outline/reference entries.
	ViewSource    FileContextView = "source"
	ViewOutline   FileContextView = "outline"
	ViewReference FileContextView = "reference"
)

// FileContextEntry is one path in the initial context catalog. Content is used
// only by Outline entries; exact source remains a separate File message so its
// ranges keep participating in coverage checks and compaction.
type FileContextEntry struct {
	Path    string
	View    FileContextView
	Reason  string // low-cardinality admission reason: unit/caller/usage_site/...
	Ref     string // optional concrete symbol/path that established the relation
	Content string
}

// FileContext makes the initial source/outline/reference choice visible to the
// model and to trajectory analysis without flattening those roles into prompt
// prose assembled by Runner.
type FileContext struct {
	entries        []FileContextEntry
	priority       int
	representation fileContextRepresentation
}

type fileContextRepresentation uint8

const (
	fileContextDetailed fileContextRepresentation = iota
	fileContextReferences
)

func NewFileContext(entries []FileContextEntry) *FileContext {
	copyEntries := make([]FileContextEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.View != ViewSource {
			copyEntries = append(copyEntries, entry)
		}
	}
	sort.SliceStable(copyEntries, func(i, j int) bool {
		if copyEntries[i].View != copyEntries[j].View {
			return fileViewRank(copyEntries[i].View) > fileViewRank(copyEntries[j].View)
		}
		return copyEntries[i].Path < copyEntries[j].Path
	})
	return &FileContext{entries: copyEntries}
}

func (c *FileContext) ToLLM() llm.Message { return c.render(c.representation) }

func (c *FileContext) render(representation fileContextRepresentation) llm.Message {
	var b strings.Builder
	b.WriteString("INITIAL FILE CONTEXT (outline/reference are navigation hints; exact source, when present, is supplied as separate file messages):\n")
	for _, entry := range c.entries {
		view := entry.View
		content := entry.Content
		if representation >= fileContextReferences && view == ViewOutline {
			view = ViewReference
			content = ""
		}
		fmt.Fprintf(&b, "- [%s] %s", view, entry.Path)
		if entry.Reason != "" {
			fmt.Fprintf(&b, " — %s", entry.Reason)
		}
		if entry.Ref != "" {
			fmt.Fprintf(&b, " (%s)", entry.Ref)
		}
		b.WriteByte('\n')
		if content != "" {
			for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
				b.WriteString("    ")
				b.WriteString(line)
				b.WriteByte('\n')
			}
		}
	}
	return llm.NewTextMessage("user", strings.TrimRight(b.String(), "\n"))
}

func (c *FileContext) Compact(expect float64) (Msg, float64) {
	next := c.clone()
	next.representation, expect = compactRepresentation(expect, c.representation, fileContextReferences, next.render)
	return next, expect
}
func (c *FileContext) Priority() int { return c.priority }

func (c *FileContext) ContextItems() []agentgo.ContextItem {
	out := make([]agentgo.ContextItem, len(c.entries))
	for i, entry := range c.entries {
		representation := entry.View
		if c.representation >= fileContextReferences && representation == ViewOutline {
			representation = ViewReference
		}
		out[i] = agentgo.ContextItem{
			ContextKey:     agentgo.ContextKey{Kind: "file", Identity: entry.Path},
			Representation: string(representation), Reason: entry.Reason, Ref: entry.Ref,
		}
	}
	return out
}

// Entries returns a copy for Harness diagnostics; callers cannot mutate the
// message after an Execution has taken ownership of it.
func (c *FileContext) Entries() []FileContextEntry {
	return append([]FileContextEntry(nil), c.entries...)
}

func (c *FileContext) clone() *FileContext {
	return &FileContext{entries: c.Entries(), priority: c.priority, representation: c.representation}
}

func fileViewRank(view FileContextView) int {
	switch view {
	case ViewOutline:
		return 2
	case ViewReference:
		return 1
	default:
		return 0
	}
}
