package runner

import (
	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/unit"
)

// Docstrings are evidence, not fixed instructions. Keep every relation and
// source reference on its own message while allowing Harness to project a
// shared body once. Unit retains the original Clues for review and diagnostics.
func separateClueDocuments(clues []unit.Clue) ([]unit.Clue, []agentgo.AgentMessage) {
	var inline []unit.Clue
	var documents []agentgo.AgentMessage
	for _, clue := range clues {
		if clue.Kind != unit.ClueDoc {
			inline = append(inline, clue)
			continue
		}
		documents = append(documents, NewClueMessage(clue))
	}
	return inline, documents
}
