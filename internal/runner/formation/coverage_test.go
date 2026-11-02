package formation

import (
	"github.com/compforge/repokit/toolkit/go"
	"github.com/qiankunli/case-code-review/internal/unit"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

func validateEdits(ch change.Change, fs []unit.Fragment) error {
	var native []repokit.Fragment
	for _, f := range fs {
		native = append(native, f.RepositoryFragment())
	}
	return repokit.ValidateFragmentEdits(ch, native)
}
