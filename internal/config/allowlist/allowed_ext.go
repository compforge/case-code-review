// Package allowedext owns CCR path classification and default review eligibility.
package allowedext

import (
	"sync"

	cg "github.com/compforge/codegraph"

	"github.com/qiankunli/case-code-review/internal/language"
)

const TestTag cg.Tag = "test"
const ReviewDataTag cg.Tag = "review_data"
const ToolingTag cg.Tag = "tooling"

// TagRules supplies the same classification to captured diffs and full scans.
// CCR extends the vocabulary without teaching CodeGraph review policy.
func TagRules() []cg.TagRule {
	var rules []cg.TagRule
	for _, rule := range cg.BuiltinTagRules() {
		switch rule.Name {
		case cg.GeneratedTag, cg.DependencyTag, cg.BuildOutputTag, cg.MinifiedTag:
			continue // The rules below include CCR's additional path conventions.
		}
		rules = append(rules, rule)
	}
	return append(rules,
		cg.TagRule{Name: TestTag, Pattern: `(?i)(^|/)([^/]*_test\.(go|py|rs)|[^/]*_spec\.rb|[^/]*tests?\.java|[^/]*\.(test|spec)\.(js|jsx|ts|tsx)|[^/]*\.test\.ets)$|(?i)(^|/)(__tests__/|src/test/java/.*\.java$|src/test/.*\.kt$)`},
		cg.TagRule{Name: ToolingTag, Pattern: `^(\.idea|\.vscode|\.svn|\.git)(/|$)`},
		cg.TagRule{Name: ReviewDataTag, Pattern: `(?i)(^|/)\.casecodereview(/|$)`},
		cg.TagRule{Name: cg.GeneratedTag, Pattern: `(?i:\.(generated\.[^/]*|gen\.go|pb\.(go|cc|h)|capnp\.(h|go|ts))$|_capnp\.(rs|py)$|(^|/)kitex_gen/.*\.go$)|(^|/)kitex_gen(/|$)`},
		cg.TagRule{Name: cg.DependencyTag, Pattern: `^(_packages|rpm|pkgs)(/|$)|(?i)(^|/)(vendor|node_modules|oh_modules|bower_components|\.pnpm-store|\.bundle|\.venv|venv|site-packages|pods|carthage)(/|$)|(?i)(^|/)\.yarn/(cache|unplugged|releases|sdks|patches)(/|$)|(?i)(^|/)\.pnp\.(cjs|loader\.mjs)$|(?i)(^|/)[^/]*\.egg-info(/|$)`},
		cg.TagRule{Name: cg.BuildOutputTag, Pattern: `(?i)(^|/)(dist|\.next|\.nuxt|\.svelte-kit|\.astro|\.docusaurus|target|\.build|obj)(/|$)|(?i)(^|/)coverage/lcov-report(/|$)`},
		cg.TagRule{Name: cg.CacheTag, Pattern: `^(\.happypack|\.cachefile)(/|$)|(?i)(^|/)(\.turbo|\.angular|\.parcel-cache|\.gradle|__pycache__|\.tox|\.mypy_cache|\.pytest_cache|\.ruff_cache|\.dart_tool|\.terraform|\.stack-work)(/|$)`},
		cg.TagRule{Name: cg.MinifiedTag, Pattern: `(?i)\.min\.(js|css)$`},
	)
}

var tagMatcher = sync.OnceValue(func() *cg.TagMatcher {
	matcher, err := cg.NewTagMatcher(TagRules())
	if err != nil {
		panic(err)
	}
	return matcher
})

func Classify(path string) []cg.Tag   { return tagMatcher().Match(path) }
func IsAllowedExt(ext string) bool    { return language.IsReviewableExtension(ext) }
func IsExcludedPath(path string) bool { return IsExcluded(Classify(path)) }

// IsExcluded consumes captured classification; it does not reclassify Changes.
func IsExcluded(tags []cg.Tag) bool {
	for _, tag := range tags {
		switch tag {
		case cg.GeneratedTag, cg.TestFixtureTag, cg.DependencyTag, cg.BuildOutputTag,
			cg.CacheTag, cg.MinifiedTag, TestTag, ReviewDataTag, ToolingTag:
			return true
		}
	}
	return false
}
