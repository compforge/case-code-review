package language

import (
	"fmt"
	"sort"
	"strings"
)

// FileOutline is a structure-preserving projection of one source file. It is
// navigation context, not evidence of runtime behavior: bodies and field
// initializers are deliberately absent.
type FileOutline struct {
	Path     string
	Language Language
	entries  []outlineEntry
	rendered string
}

// outlineMember is a language-native type-owned data declaration. Label uses
// the language's term (field, property, attribute); all are the same
// cross-language role for outline construction.
type outlineMember struct {
	Name, Owner, Label, Signature string
	Span                          Span
}

type outlineEntry struct {
	Name, Owner, Label, Signature string
	Span                          Span
}

// Outline derives a file projection from facts already produced by Analyze.
func (a Analysis) Outline(path string) FileOutline {
	outline := FileOutline{Path: path, Language: a.Language}
	for _, definition := range a.Definitions {
		outline.entries = append(outline.entries, outlineEntry{
			Name: definition.Name, Owner: definition.Owner, Label: string(definition.Kind),
			Signature: definition.Signature, Span: definition.Span,
		})
	}
	for _, member := range a.outlineMembers {
		outline.entries = append(outline.entries, outlineEntry{
			Name: member.Name, Owner: member.Owner, Label: member.Label,
			Signature: member.Signature, Span: member.Span,
		})
	}
	return outline
}

func (o FileOutline) Empty() bool { return len(o.entries) == 0 && o.rendered == "" }

// Render returns the whole-file outline in stable source order.
func (o FileOutline) Render() string {
	if o.rendered != "" {
		return o.rendered
	}
	return o.renderRange(0, 0)
}

// RenderRange keeps declarations intersecting a 1-indexed inclusive range,
// plus their owning type so the remaining members retain structural context.
func (o FileOutline) RenderRange(start, end int) string {
	if o.rendered != "" {
		return o.rendered
	}
	return o.renderRange(start, end)
}

func (o FileOutline) renderRange(start, end int) string {
	entries := append([]outlineEntry(nil), o.entries...)
	if start > 0 && end >= start {
		entries = outlineRange(entries, start, end)
	}
	if len(entries) == 0 {
		return ""
	}
	sortOutlineEntries(entries)

	byName := make(map[string]outlineEntry, len(entries))
	for _, entry := range entries {
		if outlineOwnerKind(entry.Label) {
			byName[entry.Name] = entry
		}
	}
	children := make(map[string][]outlineEntry)
	var roots []outlineEntry
	for _, entry := range entries {
		if _, ok := byName[entry.Owner]; entry.Owner != "" && ok {
			children[entry.Owner] = append(children[entry.Owner], entry)
		} else {
			roots = append(roots, entry)
		}
	}
	for owner := range children {
		sortOutlineEntries(children[owner])
	}

	var b strings.Builder
	fmt.Fprintf(&b, "File outline: %s (%s)\n", o.Path, o.Language)
	var write func(outlineEntry, int)
	write = func(entry outlineEntry, depth int) {
		b.WriteString(strings.Repeat("  ", depth))
		fmt.Fprintf(&b, "- %s %s — L%d", entry.Label, outlineSignature(entry), entry.Span.Start)
		if entry.Span.End > entry.Span.Start {
			fmt.Fprintf(&b, "-%d", entry.Span.End)
		}
		b.WriteByte('\n')
		for _, child := range children[entry.Name] {
			write(child, depth+1)
		}
	}
	for _, root := range roots {
		write(root, 0)
	}
	return strings.TrimRight(b.String(), "\n")
}

func outlineRange(entries []outlineEntry, start, end int) []outlineEntry {
	allOwners := make(map[string]outlineEntry)
	for _, entry := range entries {
		if outlineOwnerKind(entry.Label) {
			allOwners[entry.Name] = entry
		}
	}
	selected := make(map[string]bool)
	for _, entry := range entries {
		if entry.Span.End >= start && entry.Span.Start <= end {
			selected[outlineEntryKey(entry)] = true
			for owner := entry.Owner; owner != ""; {
				parent, ok := allOwners[owner]
				if !ok {
					break
				}
				selected[outlineEntryKey(parent)] = true
				owner = parent.Owner
			}
		}
	}
	var out []outlineEntry
	for _, entry := range entries {
		if selected[outlineEntryKey(entry)] {
			out = append(out, entry)
		}
	}
	return out
}

func outlineEntryKey(entry outlineEntry) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%d", entry.Label, entry.Name, entry.Span.Start, entry.Span.End)
}

func sortOutlineEntries(entries []outlineEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Span.Start != entries[j].Span.Start {
			return entries[i].Span.Start < entries[j].Span.Start
		}
		if entries[i].Span.End != entries[j].Span.End {
			return entries[i].Span.End > entries[j].Span.End
		}
		return entries[i].Name < entries[j].Name
	})
}

func outlineOwnerKind(label string) bool {
	return label == string(KindType) || label == string(KindClass) || label == string(KindInterface)
}

func outlineSignature(entry outlineEntry) string {
	if signature := strings.TrimSpace(entry.Signature); signature != "" {
		return signature
	}
	return entry.Name
}
