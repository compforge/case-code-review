// Package allowedext owns default review eligibility, independently of material classification and graph admission.
package allowedext

import (
	cg "github.com/compforge/codegraph"
	"github.com/qiankunli/case-code-review/internal/language"
	"github.com/qiankunli/case-code-review/internal/material"
)

func IsAllowedExt(ext string) bool { return language.IsReviewableExtension(ext) }

// IsExcluded consumes captured classification; it does not reclassify Changes.
// +spec=`Test source is eligible for review; generated artifacts and fixtures remain excluded by default`
// +why=`A material's role is stable even when callers override review selection`
// +link=component://docs/project.md
func IsExcluded(tags []cg.Tag) bool {
	for _, tag := range tags {
		switch tag {
		case cg.GeneratedTag, cg.TestFixtureTag, cg.DependencyTag, cg.BuildOutputTag,
			cg.CacheTag, cg.MinifiedTag, material.ReviewDataTag, cg.ToolingTag:
			return true
		}
	}
	return false
}
