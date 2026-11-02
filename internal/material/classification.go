// Package material supplies one classification vocabulary to every review snapshot consumer.
package material

import (
	"sync"

	cg "github.com/compforge/codegraph"
)

const ReviewDataTag cg.Tag = "review_data"

// TagRules extends shared naming conventions only with CCR-owned material.
// +spec=`Diff, scan and graph classify materials with the same rules; review eligibility never removes factual tags`
// +why=`Replacing a builtin tag family prevents upstream naming conventions from reaching consumers`
// +link=component://docs/project.md
func TagRules() []cg.TagRule {
	return append(cg.BuiltinTagRules(), cg.TagRule{Name: ReviewDataTag, Pattern: `(?i)(^|/)\.casecodereview(/|$)`})
}

var matcher = sync.OnceValue(func() *cg.TagMatcher {
	m, err := cg.NewTagMatcher(TagRules())
	if err != nil {
		panic(err)
	}
	return m
})

// Classify includes both format and path tags without reading file contents.
func Classify(path string) []cg.Tag {
	return matcher().DocumentTags(cg.Document{Path: path})
}
