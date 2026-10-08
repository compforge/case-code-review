package preview

import (
	"path"

	cg "github.com/compforge/codegraph"

	allowedext "github.com/qiankunli/case-code-review/internal/config/allowlist"
	"github.com/qiankunli/case-code-review/internal/config/rules"
)

// Select applies explicit caller policy before default source eligibility.
func Select(name string, tags []cg.Tag, binary bool, filter *rules.FileFilter) ExcludeReason {
	if binary {
		return ExcludeBinary
	}
	if filter != nil && filter.IsUserExcluded(name) {
		return ExcludeUserRule
	}
	if filter != nil && filter.HasInclude() && filter.IsUserIncluded(name) {
		return ExcludeNone
	}
	if ext := path.Ext(name); ext != "" && !allowedext.IsAllowedExt(ext) {
		return ExcludeExtension
	}
	if allowedext.IsExcluded(tags) {
		return ExcludeDefaultPath
	}
	return ExcludeNone
}
