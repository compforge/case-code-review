package runner

import (
	"testing"

	"github.com/qiankunli/case-code-review/internal/unit"
)

func TestCollectDossierCluesCarriesProjectRoles(t *testing.T) {
	role := unit.Clue{
		Kind: unit.ClueProject, Relation: unit.RelSelf, Ref: "cmd/server/main.go",
		Text: "component role: executable entrypoint",
	}
	got := collectDossierClues([]unit.Unit{{Clues: []unit.Clue{role}}})
	if len(got) != 1 || got[0] != role {
		t.Fatalf("Dossier clues = %+v, want entrypoint role", got)
	}
}
