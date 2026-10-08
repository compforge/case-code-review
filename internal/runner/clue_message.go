package runner

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// ClueArtifact identifies reusable prose. References and relation labels
// belong to each message: identical prose can support several source identities
// without repeating its body. Snapshot provenance belongs to each occurrence,
// not the reusable prose; different bodies always retain different identities.
type ClueArtifact struct {
	ClueKind unit.ClueKind `codec:"clue_kind"`
	Text     string        `codec:"text"`
}

func (d ClueArtifact) ID() string {
	encoded, _ := json.Marshal(d)
	return fmt.Sprintf("clue:%x", sha256.Sum256(encoded))
}
func (ClueArtifact) Kind() string { return "clue" }

// ClueMessage is the review-domain projection of a Clue. It owns retention
// policy; Harness only sees the material projection capability.
type ClueMessage struct {
	unit.Clue
	timestamp   time.Time
	label       string
	reference   bool
	duplicateOf string // request-local; recomputed after every context rewrite
}

func NewClueMessage(clue unit.Clue) *ClueMessage {
	return &ClueMessage{Clue: clue, timestamp: time.Now(), label: string(clue.Relation)}
}
func (d *ClueMessage) GetTimestamp() time.Time { return d.timestamp }
func (d *ClueMessage) MaterialArtifact() agentgo.Artifact {
	if d.Text == "" {
		return nil
	}
	return ClueArtifact{ClueKind: d.Kind, Text: d.Text}
}
func (d *ClueMessage) MaterialReference() string {
	ref := d.Ref
	if d.Snapshot != "" {
		ref += " (before " + d.Snapshot + ")"
	}
	return ref
}
func (d *ClueMessage) MaterialVisible() bool {
	// Only docstrings currently have a body-reuse policy. Other clues retain
	// their full contract, rule or finding until a domain policy is defined.
	return d.Kind == unit.ClueDoc && !d.reference && d.duplicateOf == "" && d.Text != ""
}
func (d *ClueMessage) WithMaterialReference(ref string) msg.MaterialMessage {
	copy := *d
	copy.duplicateOf = ref
	return &copy
}

var _ msg.MaterialMessage = (*ClueMessage)(nil)

func (d *ClueMessage) render() llm.Message {
	header := "Clue (" + string(d.Kind) + "): " + d.MaterialReference()
	if d.label != "" {
		header += " — " + strings.TrimSpace(d.label)
	}
	body := d.Text
	if d.reference {
		body = "[Compacted to a reference; retrieve the source document if needed.]"
	}
	if d.duplicateOf != "" {
		body = "[Same body: " + d.duplicateOf + "]"
	}
	return llm.NewTextMessage("user", header+"\n"+body)
}
func (d *ClueMessage) ToLLM() llm.Message      { return d.render() }
func (d *ClueMessage) GetRole() agentgo.Role   { return agentgo.RoleUser }
func (d *ClueMessage) TextContent() string     { rendered := d.render(); return rendered.ExtractText() }
func (d *ClueMessage) ThinkingContent() string { return "" }
func (d *ClueMessage) HasToolCalls() bool      { return false }
func (d *ClueMessage) ToMessage() (agentgo.Message, bool) {
	return agentgo.Message{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock(d.TextContent())}, Timestamp: d.timestamp}, true
}
func (d *ClueMessage) Raw() agentgo.AgentMessage {
	copy := *d
	copy.reference = false
	copy.duplicateOf = ""
	return &copy
}
func (d *ClueMessage) Priority() int { return 0 }
func (d *ClueMessage) Compact(expect float64) (agentgo.AgentMessage, float64) {
	copy := *d
	copy.duplicateOf = ""
	if d.Kind != unit.ClueDoc {
		return &copy, 1
	}
	rawTokens := llm.CountTokens(d.Raw().TextContent())
	currentTokens := llm.CountTokens(copy.TextContent())
	if float64(currentTokens) > float64(rawTokens)*expect {
		candidate := copy
		candidate.reference = true
		if next := llm.CountTokens(candidate.TextContent()); next < currentTokens {
			copy, currentTokens = candidate, next
		}
	}
	return &copy, float64(currentTokens) / float64(max(rawTokens, 1))
}
func (d *ClueMessage) ContextItems() []agentgo.ContextItem {
	representation := "full"
	if d.reference || d.duplicateOf != "" {
		representation = "reference"
	}
	return []agentgo.ContextItem{{ContextKey: agentgo.ContextKey{Kind: "clue", Identity: (ClueArtifact{ClueKind: d.Kind, Text: d.Text}).ID()}, Representation: representation, Ref: d.Ref, Reason: d.label}}
}
