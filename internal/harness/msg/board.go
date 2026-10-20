package msg

import (
	"github.com/compforge/agentgo"
)

// Board is AgentGo Unit Review's digest of peer units' bulletins. Like
// File it is re-derivable — the content came from the board and can be pulled
// again — so its compact form is a reference to fresh notes.
type Board struct {
	messageMeta
	digest         string
	representation boardRepresentation
}

type boardRepresentation uint8

const (
	boardDigest boardRepresentation = iota
	boardReference
)

// NewBoard wraps a rendered board digest as an evictable user message.
func NewBoard(digest string) *Board { return &Board{messageMeta: newMessageMeta(), digest: digest} }

func (b *Board) renderCurrent() agentgo.Message {
	return b.render(b.representation)
}

func (b *Board) GetRole() agentgo.Role { return domainRole(b.renderCurrent()) }
func (b *Board) Raw() agentgo.AgentMessage {
	raw := *b
	raw.representation = boardDigest
	return &raw
}
func (b *Board) TextContent() string     { return domainText(b.renderCurrent()) }
func (b *Board) ThinkingContent() string { return "" }
func (b *Board) HasToolCalls() bool      { return domainHasToolCalls(b.renderCurrent()) }
func (b *Board) ToMessage() (agentgo.Message, bool) {
	return domainToMessage(b.renderCurrent(), "", b.GetTimestamp())
}

func (b *Board) render(representation boardRepresentation) agentgo.Message {
	if representation >= boardReference {
		return agentgo.Message{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock("(peer-unit board notes compacted; fresh notes can be pulled again)")}}
	}
	return agentgo.Message{Role: agentgo.RoleUser, Content: []agentgo.ContentBlock{agentgo.TextBlock(b.digest)}}
}

func (b *Board) Compact(expect float64) (agentgo.AgentMessage, float64) {
	next := *b
	next.representation, expect = compactRepresentation(expect, b.representation, boardReference, next.render)
	return &next, expect
}

func (b *Board) Priority() int { return 0 }
