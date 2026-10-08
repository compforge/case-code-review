package msg

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/compforge/agentgo"
)

// DiffArtifact preserves change semantics and exact source observations.
// This immutable payload is CCR policy, not a restriction on AgentGo artifacts.
type DiffArtifact struct {
	Paths   []string         `codec:"paths"`
	Text    string           `codec:"text"`
	Sources []SourceArtifact `codec:"sources"`
}

func (d DiffArtifact) ID() string {
	encoded, _ := json.Marshal(d)
	return fmt.Sprintf("diff:%x", sha256.Sum256(encoded))
}
func (DiffArtifact) Kind() string { return "diff" }

// ConfigureReviewSource makes a task's primary diff required evidence. Exact
// hunk observations can cover later source reads without discarding +/- syntax.
func (d *Diff) ConfigureReviewSource(sources []SourceArtifact) *Diff {
	d.sources = append([]SourceArtifact(nil), sources...)
	d.required = true
	return d
}
func (d Diff) RequiredMaterial() bool { return d.required }
func (d Diff) MaterialArtifact() agentgo.Artifact {
	return DiffArtifact{Paths: d.Paths, Text: d.Content, Sources: d.sources}
}
func (d Diff) MaterialReference() string                    { return d.Label }
func (d Diff) MaterialVisible() bool                        { return false } // Change syntax remains explicit.
func (d Diff) WithMaterialReference(string) MaterialMessage { return d }
func (d Diff) ContextItems() []agentgo.ContextItem {
	representation := "full"
	if d.representation != diffFull {
		representation = "reference"
	}
	return []agentgo.ContextItem{{ContextKey: agentgo.ContextKey{Kind: "diff", Identity: d.MaterialArtifact().ID()}, Representation: representation, Ref: d.Label}}
}
