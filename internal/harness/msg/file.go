package msg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/qiankunli/case-code-review/internal/llm"
)

// File is a typed message for file content in the conversation — a read_files
// tool result, or an initial source preload. It keeps the identity a wire message
// erases — which path, which line range, what content, which tool call it
// answers — so the loop can reason about file content as content: deduplicate
// a re-read of the same range, and evict by re-derivability when the context
// tightens (file content is the one thing that can always be fetched again).
//
// Deliberately NO wire form is stored: the identity is content + pairing, and
// the wire SHAPE (tool_result vs user text) is ToLLM's rendering decision —
// the precondition for ever A/B-ing per-provider forms (see docs/harness.md).
// Today the decision is fixed: paired content renders as the
// tool_result it answers, unpaired as user text.
//
// A File is held by pointer so the retained llmloop can still stub it in place.
// Active ContextManager keeps the value immutable and raises an envelope's
// CompactionLevel instead: projection swaps lowered text for a one-line pointer
// while keeping the message's position and
// tool_call pairing — the 1:1 lowering invariant and the wire protocol's
// call/result pairing both stay intact.
type File struct {
	Path       string
	Start, End int    // 1-indexed inclusive line range actually shown
	Total      int    // total lines in the file at read time
	Content    string // the rendered block (read_files's numbered-line format)
	Snapshot   FileSnapshot
	Ref        string // populated for baseline snapshots when the provider reports it
	// Label describes why this file is present without leaking Unit or Clue
	// objects into Harness, e.g. "code under review" or "related caller ...".
	Label string
	// CondensedContent is an optional producer-authored lower-cost rendering.
	// Harness never parses source/JSON/TOML to invent this representation.
	CondensedContent string
	priority         int

	toolCallID string     // non-empty: entered via the tool protocol; pairing must survive
	stubbed    StubReason // "" = full content
}

type FileSnapshot string

const (
	SnapshotCurrent  FileSnapshot = "current"
	SnapshotBaseline FileSnapshot = "baseline"
)

// StubReason selects the pointer text a stubbed File lowers to — the model
// must know WHY content vanished: a superseded copy points forward to the
// newer read; an evicted one says how to get the content back.
type StubReason string

const (
	// StubSuperseded: a later read covers this one; the content is below.
	StubSuperseded StubReason = "superseded"
	// StubEvicted: elided under token pressure; re-derivable via read_files.
	StubEvicted StubReason = "evicted"
)

// ToLLM renders the full content, or — once stubbed — a pointer that spends no
// meaningful tokens. Shape selection lives HERE, not at construction: paired
// content answers its tool call, unpaired content is user text.
func (f *File) ToLLM(level CompactionLevel) llm.Message {
	text := f.render(level)
	if f.toolCallID != "" {
		return llm.NewToolResultMessage(f.toolCallID, text)
	}
	return llm.NewTextMessage("user", text)
}

func (f *File) render(level CompactionLevel) string {
	text := f.Content
	if level == CompactionCondensed && f.CondensedContent != "" {
		text = f.CondensedContent
	}
	if f.Label != "" && level < CompactionReference {
		text = addContextLabel(text, f.Label)
	}
	if level == CompactionReference {
		toolName := f.ToolName()
		text = fmt.Sprintf("File: %s lines %d-%d (%s snapshot) — compacted to a reference; call %s if the content is needed again.",
			f.Path, f.Start, f.End, f.Snapshot, toolName)
	}
	switch f.stubbed {
	case StubSuperseded:
		text = fmt.Sprintf("File: %s lines %d-%d — superseded by a later read of the same content below; elided.",
			f.Path, f.Start, f.End)
	case StubEvicted:
		text = fmt.Sprintf("File: %s lines %d-%d — elided to fit the context budget; call read_files again if you still need it.",
			f.Path, f.Start, f.End)
	}
	return text
}

// FromLLM restores a file tool result into this message. Keep it beside ToLLM:
// changes to the file wire contract must update both directions together.
func (f *File) FromLLM(result LLMToolResult) bool {
	if result.failed() || (result.Tool != FileReadToolName && result.Tool != FileReadBaseToolName) {
		return false
	}
	m := fileReadHeader.FindStringSubmatch(result.Content)
	if m == nil {
		return false
	}
	total, err1 := strconv.Atoi(m[2])
	start, err2 := strconv.Atoi(m[3])
	end, err3 := strconv.Atoi(m[4])
	if err1 != nil || err2 != nil || err3 != nil || start < 1 || end < start {
		return false
	}
	snapshot := SnapshotCurrent
	ref := ""
	if result.Tool == FileReadBaseToolName {
		snapshot = SnapshotBaseline
		if refMatch := baselineRefHeader.FindStringSubmatch(result.Content); refMatch != nil {
			ref = strings.TrimSpace(refMatch[1])
		}
	}
	*f = File{
		Path:       strings.TrimSpace(m[1]),
		Start:      start,
		End:        end,
		Total:      total,
		Content:    result.Content,
		Snapshot:   snapshot,
		Ref:        ref,
		toolCallID: result.ToolCallID,
	}
	return true
}

func (f *File) Lower() llm.Message { return f.ToLLM(CompactionNone) }

func (f *File) MaxCompaction() CompactionLevel { return CompactionReference }
func (f *File) Priority() int                  { return f.priority }

func (f *File) ToolName() string {
	if f.Snapshot == SnapshotBaseline {
		return FileReadBaseToolName
	}
	return FileReadToolName
}

// Stub elides the content with the given reason (idempotent; the first reason
// wins — a superseded copy staying "superseded" under later eviction pressure
// keeps its forward pointer meaningful).
func (f *File) Stub(reason StubReason) {
	if f.stubbed == "" {
		f.stubbed = reason
	}
}

// Stubbed reports whether the content has been elided.
func (f *File) Stubbed() bool { return f.stubbed != "" }

// FullContentVisible reports whether this projection still contains the exact
// source range. A producer-authored condensed form such as FileOutline is
// useful context, but must not suppress a later read_files request for source.
func (f *File) FullContentVisible(level CompactionLevel) bool {
	if f.Stubbed() || level >= CompactionReference {
		return false
	}
	return level != CompactionCondensed || f.CondensedContent == ""
}

// Reclaim / Reclaimed implement msg.Reclaimable: eviction under token pressure
// is the evicted-reason stub. (Dedup uses Stub(StubSuperseded) directly.)
func (f *File) Reclaim()        { f.Stub(StubEvicted) }
func (f *File) Reclaimed() bool { return f.Stubbed() }

// Covers reports whether f's range contains g's range of the same path — the
// dedup precondition: everything g shows, f shows too.
func (f *File) Covers(g *File) bool {
	return f.Path == g.Path && f.Snapshot == g.Snapshot && f.Ref == g.Ref &&
		f.Start <= g.Start && f.End >= g.End
}

// fileReadHeader matches the read_files tool's response header, which is the
// tool's OUTPUT CONTRACT (internal/harness/tool/read_files.go): a "File:" line with the
// path and total, then a LINE_RANGE line with the displayed range. Parsing the
// result (rather than the tool-call arguments) keeps this independent of
// default-filling logic — the header states what was actually shown.
var fileReadHeader = regexp.MustCompile(`(?m)^File: (.+) \(Total lines: (\d+)\)\nIS_TRUNCATED: (?:true|false)\nLINE_RANGE: (\d+)-(\d+)\n`)

var baselineRefHeader = regexp.MustCompile(`(?m)^Baseline ref: (.+)$`)

// visibleFileHeader recognizes both read_files results and preloaded File
// messages. Preloaded files omit IS_TRUNCATED and, for whole files, LINE_RANGE;
// in that shape the header's total is the visible 1..N range.
var visibleFileHeader = regexp.MustCompile(`(?m)^File: (.+) \(Total lines: (\d+)\)\n(?:IS_TRUNCATED: (?:true|false)\n)?(?:LINE_RANGE: (\d+)-(\d+)\n)?`)

// FileReadToolName is the tool whose results are promoted to File messages.
const FileReadToolName = "read_files"
const FileReadBaseToolName = "read_base_files"

// NewFile builds a File whose content entered the conversation OUTSIDE the
// tool protocol — an initial source preload. Same identity, same dedup/evict
// participation; it just has no tool_call pairing to preserve.
func NewFile(path string, start, end, total int, content string) *File {
	return &File{
		Path: path, Start: start, End: end, Total: total, Content: content,
		Snapshot: SnapshotCurrent,
	}
}

// ConfigurePresentation attaches execution-facing display policy. The caller
// supplies semantic variants while ContextManager remains the only level owner.
func (f *File) ConfigurePresentation(label, condensed string) *File {
	f.Label = label
	f.CondensedContent = condensed
	return f
}

// ConfigurePriority sets execution-local retention importance without making
// it part of the immutable repository snapshot stored on a Unit.
func (f *File) ConfigurePriority(priority int) *File {
	f.priority = priority
	return f
}

// IsToolResult reports whether this File entered through read_files rather than
// the initial source context. Harness uses the distinction only for diagnostics and
// duplicate-read guidance; both sources share the same context lifecycle.
func (f *File) IsToolResult() bool { return f.toolCallID != "" }

// VisibleFileRange recovers the path and range visibly present in a lowered
// File message. It is used after context projection, where the typed File may
// already have been lowered by the compression engine.
func VisibleFileRange(text string) (path string, start, end, total int, ok bool) {
	m := visibleFileHeader.FindStringSubmatch(text)
	if m == nil {
		return "", 0, 0, 0, false
	}
	total, err := strconv.Atoi(m[2])
	if err != nil || total < 1 {
		return "", 0, 0, 0, false
	}
	start, end = 1, total
	if m[3] != "" || m[4] != "" {
		start, err = strconv.Atoi(m[3])
		if err != nil {
			return "", 0, 0, 0, false
		}
		end, err = strconv.Atoi(m[4])
		if err != nil || start < 1 || end < start {
			return "", 0, 0, 0, false
		}
	}
	return strings.TrimSpace(m[1]), start, end, total, true
}

func addContextLabel(content, label string) string {
	insertAt := strings.IndexByte(content, '\n')
	if insertAt < 0 {
		return content + "\nCONTEXT: " + label
	}
	insertAt++
	// Keep the read_files header contiguous. Visible-range detection and tool
	// protocol diagnostics intentionally share that stable header contract.
	for _, prefix := range []string{"IS_TRUNCATED: ", "LINE_RANGE: "} {
		if !strings.HasPrefix(content[insertAt:], prefix) {
			continue
		}
		next := strings.IndexByte(content[insertAt:], '\n')
		if next < 0 {
			insertAt = len(content)
			break
		}
		insertAt += next + 1
	}
	return content[:insertAt] + "CONTEXT: " + label + "\n" + content[insertAt:]
}

// VisibleFileLabel returns the semantic label carried by a lowered File.
func VisibleFileLabel(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "CONTEXT: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "CONTEXT: "))
		}
	}
	return ""
}

// DedupFiles stubs every earlier un-stubbed File whose range is covered by a
// LATER read of the same path — the model keeps the newest copy (nearest to
// the conversation tail, least likely compressed away) and pays for the
// content once. Line-shift safety: reads at different times could see
// different file states, but within one review loop the workspace/ref is
// fixed, so same path + covered range ⇒ same content.
func DedupFiles(messages []Msg) (stubbed int) {
	var files []*File
	for _, message := range messages {
		files = append(files, filesInMessage(message)...)
	}
	for i := len(files) - 1; i >= 0; i-- {
		newer := files[i]
		if newer.Stubbed() {
			continue
		}
		for j := range i {
			older := files[j]
			if older.Stubbed() {
				continue
			}
			if newer.Covers(older) {
				older.Stub(StubSuperseded)
				stubbed++
			}
		}
	}
	return stubbed
}

func filesInMessage(message Msg) []*File {
	switch value := message.(type) {
	case *File:
		return []*File{value}
	case *FileBatch:
		return value.Files()
	default:
		return nil
	}
}
