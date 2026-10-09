package msg

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/tool"
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
// A File is held by pointer while ContextManager treats the value immutably and
// passes its target ratio to Compact: File selects source, outline, or path
// itself while keeping the
// message's position and
// tool_call pairing — the 1:1 lowering invariant and the wire protocol's
// call/result pairing both stay intact.
type File struct {
	messageMeta
	Path       string
	Start, End int    // 1-indexed inclusive line range actually shown
	Total      int    // total lines in the file at read time
	Content    string // the rendered block (read_files's numbered-line format)
	Snapshot   FileSnapshot
	Ref        string // baseline revision or versioned dependency content identity
	// Label describes why this file is present without leaking Unit or Clue
	// objects into Harness, e.g. "code under review" or "related caller ...".
	Label string
	// ContextReason/ContextRef retain the structured admission provenance used
	// by Initial Context evaluation. Label remains presentation-only.
	ContextReason string
	ContextRef    string
	// Outline is the producer-authored structural view. Harness never parses
	// source/JSON/TOML to invent it.
	Outline        string
	representation fileRepresentation
	priority       int

	toolCallID string     // non-empty: entered via the tool protocol; pairing must survive
	stubbed    StubReason // "" = full content
}

type fileRepresentation uint8

const (
	fileSource fileRepresentation = iota
	fileOutline
	fileReference
)

type FileSnapshot string

const (
	SnapshotCurrent    FileSnapshot = "current"
	SnapshotBaseline   FileSnapshot = "baseline"
	SnapshotDependency FileSnapshot = "dependency"
)

// StubReason selects the pointer text a deduplicated File lowers to.
type StubReason string

const (
	// StubSuperseded: a later read covers this one; the content is below.
	StubSuperseded StubReason = "superseded"
)

// ToLLM renders the current semantic projection.
func (f *File) ToLLM() llm.Message {
	text := f.render()
	if f.toolCallID != "" {
		return llm.NewToolResultMessage(f.toolCallID, text)
	}
	return llm.NewTextMessage("user", text)
}

func (f *File) GetRole() agentgo.Role { return domainRole(f.ToLLM()) }

// Raw restores the complete source projection without changing the current
// compacted value.
//
// +spec=`Raw always returns the full original file source; it must not mutate or preserve an outline/reference/superseded projection`
// +case:id=raw_restores_source,desc=`Raw is called on a compacted file`,expect=`returned message contains full source and receiver remains compacted`
func (f *File) Raw() agentgo.AgentMessage {
	raw := *f
	raw.representation = fileSource
	raw.stubbed = ""
	return &raw
}

func (f *File) TextContent() string     { return domainText(f.ToLLM()) }
func (f *File) ThinkingContent() string { return "" }
func (f *File) HasToolCalls() bool      { return domainHasToolCalls(f.ToLLM()) }
func (f *File) ToMessage() (agentgo.Message, bool) {
	return domainToMessage(f.ToLLM(), f.ToolName(), f.GetTimestamp())
}

func (f *File) render() string {
	representation := f.representation
	text := f.Content
	if representation == fileOutline && f.Outline != "" {
		text = f.Outline
	}
	if f.Label != "" && representation < fileReference {
		text = addContextLabel(text, f.Label)
	}
	if representation == fileReference {
		toolName := f.ToolName()
		text = fmt.Sprintf("File: %s lines %d-%d (%s snapshot) — compacted to a reference; call %s if the content is needed again.",
			f.Path, f.Start, f.End, f.Snapshot, toolName)
		if f.Snapshot == SnapshotDependency {
			text += " Ref: " + f.Ref
		}
	}
	switch f.stubbed {
	case StubSuperseded:
		text = fmt.Sprintf("File: %s lines %d-%d — superseded by a later read of the same content below; elided.",
			f.Path, f.Start, f.End)
	}
	return text
}

// FromLLM restores a file tool result into this message. Keep it beside ToLLM:
// changes to the file wire contract must update both directions together.
func (f *File) FromLLM(result LLMToolResult) bool {
	if result.Tool == tool.ReadGoDependency.Name() && !result.failed() {
		source, ok := tool.DecodeGoDependencyResult(result.Content)
		if !ok {
			return false
		}
		*f = File{messageMeta: result.messageMeta(), Path: source.Path, Start: source.Start, End: source.End, Total: source.Total, Content: source.Content, Snapshot: SnapshotDependency, Ref: source.Ref, toolCallID: result.ToolCallID}
		return true
	}
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
		messageMeta: result.messageMeta(),
		Path:        strings.TrimSpace(m[1]),
		Start:       start,
		End:         end,
		Total:       total,
		Content:     result.Content,
		Snapshot:    snapshot,
		Ref:         ref,
		toolCallID:  result.ToolCallID,
	}
	return true
}

func (f *File) Priority() int { return f.priority }

// Compact selects the first File-owned representation that satisfies expect.
// Ratios are always measured against the full source representation, including
// the context label exactly as it would be sent to the model.
// Compact selects a semantic projection without mutating the full message.
//
// +spec=`Compact is immutable and selects source, producer outline, then path reference according to the requested ratio; actual is measured against full source`
// +case:id=outline_before_path,desc=`ratio fits outline but not source`,expect=`outline projection is returned and Raw still returns source`
func (f *File) Compact(expect float64) (agentgo.AgentMessage, float64) {
	rawTokens := llm.CountTokens(f.renderRepresentation(fileSource))
	if rawTokens <= 0 {
		return f, 1
	}
	currentTokens := llm.CountTokens(f.render())
	currentRatio := float64(currentTokens) / float64(rawTokens)
	if currentRatio <= expect {
		return f, currentRatio
	}

	next := *f
	for representation := f.representation + 1; representation <= fileReference; representation++ {
		if representation == fileOutline && f.Outline == "" {
			continue
		}
		next.representation = representation
		currentTokens = llm.CountTokens(next.render())
		currentRatio = float64(currentTokens) / float64(rawTokens)
		if currentRatio <= expect {
			return &next, currentRatio
		}
	}
	return &next, currentRatio
}

func (f *File) renderRepresentation(representation fileRepresentation) string {
	view := *f
	view.representation = representation
	return view.render()
}

func (f *File) ContextItems() []agentgo.ContextItem {
	reason := f.ContextReason
	if reason == "" && f.IsToolResult() {
		reason = "prior_read"
	}
	representation := ViewSource
	effective := f.representation
	if f.Stubbed() || effective >= fileReference {
		representation = ViewReference
	} else if effective == fileOutline && f.Outline != "" {
		representation = ViewOutline
	}
	kind := "file"
	identity, ref := f.Path, f.ContextRef
	if f.Snapshot == SnapshotBaseline {
		kind = "baseline_file"
	} else if f.Snapshot == SnapshotDependency {
		kind, identity, ref = "dependency_file", f.Ref, f.Ref
	}
	return []agentgo.ContextItem{{
		ContextKey:     agentgo.ContextKey{Kind: kind, Identity: identity},
		Representation: string(representation), Reason: reason, Ref: ref,
	}}
}

func (f *File) ToolName() string {
	if f.Snapshot == SnapshotDependency {
		return tool.ReadGoDependency.Name()
	}
	if f.Snapshot == SnapshotBaseline {
		return FileReadBaseToolName
	}
	return FileReadToolName
}

// Stubbed reports whether the content has been elided.
func (f *File) Stubbed() bool { return f.stubbed != "" }

// FullContentVisible reports whether this projection still contains the exact
// source range. A producer-authored condensed form such as FileOutline is
// useful context, but must not suppress a later read_files request for source.
func (f *File) FullContentVisible() bool {
	effective := f.representation
	if f.Stubbed() || effective >= fileReference {
		return false
	}
	return effective != fileOutline || f.Outline == ""
}

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
		messageMeta: newMessageMeta(),
		Path:        path, Start: start, End: end, Total: total, Content: content,
		Snapshot: SnapshotCurrent,
	}
}

// ConfigurePresentation attaches execution-facing display policy and the
// producer-authored outline used by File's ratio-based projection.
func (f *File) ConfigurePresentation(label, outline string) *File {
	f.Label = label
	f.Outline = outline
	return f
}

// ConfigureContext records why an initial source snapshot was admitted. Tool
// results leave this empty because their provenance is the tool call itself.
func (f *File) ConfigureContext(reason, ref string) *File {
	f.ContextReason = reason
	f.ContextRef = ref
	return f
}

// ConfigurePriority sets execution-local retention importance without making
// it part of the immutable repository snapshot stored on a Unit.
func (f *File) ConfigurePriority(priority int) *File {
	if f.timestamp.IsZero() {
		f.messageMeta = newMessageMeta()
	}
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
// DedupFiles returns an immutable projection in which earlier covered file
// reads are replaced by forward references. Raw on every projected message
// still restores its original full content.
func DedupFiles(messages []agentgo.AgentMessage) ([]agentgo.AgentMessage, int) {
	type fileLocation struct {
		message int
		item    int
		file    *File
	}
	var files []fileLocation
	for messageIndex, message := range messages {
		switch value := message.(type) {
		case *File:
			files = append(files, fileLocation{message: messageIndex, item: -1, file: value})
		case *FileBatch:
			for itemIndex, item := range value.items {
				if item.file != nil {
					files = append(files, fileLocation{message: messageIndex, item: itemIndex, file: item.file})
				}
			}
		}
	}
	stubbed := make(map[int]map[int]bool)
	for i := len(files) - 1; i >= 0; i-- {
		newer := files[i].file
		if newer.Stubbed() {
			continue
		}
		for j := range i {
			older := files[j].file
			if older.Stubbed() {
				continue
			}
			if newer.Covers(older) {
				if stubbed[files[j].message] == nil {
					stubbed[files[j].message] = make(map[int]bool)
				}
				stubbed[files[j].message][files[j].item] = true
			}
		}
	}
	if len(stubbed) == 0 {
		return messages, 0
	}
	out := append([]agentgo.AgentMessage(nil), messages...)
	count := 0
	for messageIndex, items := range stubbed {
		switch value := messages[messageIndex].(type) {
		case *File:
			copyFile := *value
			copyFile.stubbed = StubSuperseded
			out[messageIndex] = &copyFile
			count++
		case *FileBatch:
			copyBatch := value.clone()
			for itemIndex := range items {
				copyBatch.items[itemIndex].file.stubbed = StubSuperseded
				count++
			}
			out[messageIndex] = copyBatch
		}
	}
	return out, count
}
