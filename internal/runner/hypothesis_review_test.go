package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestCollectCaseFileCluesCarriesProjectRoles(t *testing.T) {
	role := unit.Clue{
		Kind: unit.ClueProject, Relation: unit.RelSelf, Ref: "cmd/server/main.go",
		Text: "component role: executable entrypoint",
	}
	got := collectCaseFileClues([]unit.Unit{{Clues: []unit.Clue{role}}})
	if len(got) != 1 || got[0] != role {
		t.Fatalf("CaseFile clues = %+v, want entrypoint role", got)
	}
}
