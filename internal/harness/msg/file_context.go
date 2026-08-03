package msg

import (
	"fmt"
	"sort"
	"strings"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/llm"
)

// FileContextView describes how much of a statically related file was placed
// in the initial request. Only ViewSource is evidence-bearing source; Outline
// and Reference are navigation hints and must never suppress read_files.
type FileContextView string

const (
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
	entries  []FileContextEntry
	priority int
}

func NewFileContext(entries []FileContextEntry) *FileContext {
	copyEntries := append([]FileContextEntry(nil), entries...)
	sort.SliceStable(copyEntries, func(i, j int) bool {
		if copyEntries[i].View != copyEntries[j].View {
			return fileViewRank(copyEntries[i].View) > fileViewRank(copyEntries[j].View)
		}
		return copyEntries[i].Path < copyEntries[j].Path
	})
	return &FileContext{entries: copyEntries}
}

func (c *FileContext) ToLLM(level CompactionLevel) llm.Message {
	var b strings.Builder
	b.WriteString("INITIAL FILE CONTEXT (source was supplied as exact code; outline/reference are navigation hints; use the current file inventory for present coverage):\n")
	for _, entry := range c.entries {
		view := entry.View
		content := entry.Content
		if level >= CompactionCondensed && view == ViewOutline {
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

func (c *FileContext) MaxCompaction() CompactionLevel { return CompactionCondensed }
func (c *FileContext) Priority() int                  { return c.priority }

func (c *FileContext) ContextItems(level CompactionLevel) []agentgo.ContextItem {
	out := make([]agentgo.ContextItem, len(c.entries))
	for i, entry := range c.entries {
		representation := entry.View
		if level >= CompactionCondensed && representation == ViewOutline {
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
	return &FileContext{entries: c.Entries(), priority: c.priority}
}

func fileViewRank(view FileContextView) int {
	switch view {
	case ViewSource:
		return 3
	case ViewOutline:
		return 2
	case ViewReference:
		return 1
	default:
		return 0
	}
}
