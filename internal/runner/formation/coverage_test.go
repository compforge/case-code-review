package formation

import (
	"github.com/compforge/repocli/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func validateEdits(ch change.Change, fs []unit.Fragment) error {
	var native []repocli.Fragment
	for _, f := range fs {
		native = append(native, f.RepositoryFragment())
	}
	return repocli.ValidateFragmentEdits(ch, native)
}
