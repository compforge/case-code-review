package msg

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/compforge/agentgo"
)

// SourceArtifact is an immutable observation of exact source lines. Snapshot,
// ref, path and content all participate in identity: a later read may overlap
// an earlier one without replacing its evidence. Outline and clipped lines do
// not establish source coverage. Fields are portable via AgentGo's codec.
type SourceArtifact struct {
	Path     string               `codec:"path"`
	Snapshot FileSnapshot         `codec:"snapshot"`
	Ref      string               `codec:"ref,omitempty"`
	Lines    []SourceArtifactLine `codec:"lines"`
}

type SourceArtifactLine struct {
	Number int    `codec:"number"`
	Text   string `codec:"text"`
}

func (a SourceArtifact) ID() string {
	// This concrete value has only JSON-encodable fields. Tuple encoding avoids
	// ambiguous delimiters in paths or refs; the digest includes exact contents.
	encoded, _ := json.Marshal(a)
	return fmt.Sprintf("source:%x", sha256.Sum256(encoded))
}
func (SourceArtifact) Kind() string { return "source" }

func sourceArtifact(text, path string, snapshot FileSnapshot, ref string) SourceArtifact {
	a := SourceArtifact{Path: path, Snapshot: snapshot, Ref: ref}
	if path == "" {
		return a
	}
	lines := strings.SplitAfter(text, "\n")
	for i, line := range lines {
		number, body, ok := strings.Cut(strings.TrimSuffix(line, "\n"), "|")
		n, err := strconv.Atoi(number)
		if !ok || err != nil || n < 1 {
			continue
		}
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "[Output truncated:") {
			continue
		}
		a.Lines = append(a.Lines, SourceArtifactLine{Number: n, Text: body})
	}
	return a
}

// SourceArtifacts extracts material independently of the current projection.
// Raw is used only at registration; registered content is never treated as
// proof that a compacted message still displays that content to the model.
func SourceArtifacts(messages []agentgo.AgentMessage) []agentgo.Artifact {
	values := make(map[string]agentgo.Artifact)
	add := func(text, path string, snapshot FileSnapshot, ref string) {
		a := sourceArtifact(text, path, snapshot, ref)
		if len(a.Lines) > 0 {
			values[a.ID()] = a
		}
	}
	addFile := func(file *File) { add(file.Content, file.Path, file.Snapshot, file.Ref) }
	for _, message := range messages {
		switch value := message.Raw().(type) {
		case Diff:
			for _, source := range value.sources {
				values[source.ID()] = source
			}
		case *File:
			addFile(value)
		case *FileBatch:
			for _, item := range value.items {
				if item.file != nil {
					addFile(item.file)
				}
			}
		case *SearchBatch:
			// sharedSource is produced by the search tool's source contract; match
			// summaries and navigation prose are not parsed into source evidence.
			path := ""
			var block strings.Builder
			flush := func() { add(block.String(), path, SnapshotCurrent, ""); block.Reset() }
			for _, line := range strings.SplitAfter(value.sharedSource, "\n") {
				if strings.HasPrefix(line, "File: ") {
					flush()
					path = strings.TrimSpace(strings.TrimPrefix(line, "File: "))
				}
				block.WriteString(line)
			}
			flush()
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]agentgo.Artifact, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

// Artifacts collects independently addressable material from typed messages.
func Artifacts(messages []agentgo.AgentMessage) []agentgo.Artifact {
	values := SourceArtifacts(messages)
	seen := make(map[string]bool)
	for _, message := range messages {
		if material, ok := message.Raw().(MaterialMessage); ok {
			artifact := material.MaterialArtifact()
			if artifact != nil && !seen[artifact.ID()] {
				values = append(values, artifact)
				seen[artifact.ID()] = true
			}
		}
	}
	return values
}

func RegisterArtifacts(manager agentgo.ArtifactManager, messages []agentgo.AgentMessage) error {
	for _, value := range Artifacts(messages) {
		// Immutable content IDs make duplicate registration safe, including parallel
		// tools. Never read-modify-write a shared file record and lose another range.
		if err := manager.AddArtifact(value, false); err != nil && !errors.Is(err, agentgo.ErrArtifactExists) {
			return err
		}
	}
	return nil
}

// Registered observations establish identity; each TransformSource invocation
// separately determines which of their lines are actually visible this time.
type sourceInventory map[sourceLine]map[string]bool

func registeredSource(artifacts []agentgo.Artifact) sourceInventory {
	inventory := make(sourceInventory)
	for _, value := range artifacts {
		source, ok := value.(SourceArtifact)
		if !ok {
			continue
		}
		for _, line := range source.Lines {
			key := sourceLine{source.Path, source.Snapshot, source.Ref, line.Number}
			if inventory[key] == nil {
				inventory[key] = make(map[string]bool)
			}
			inventory[key][line.Text] = true
		}
	}
	return inventory
}
