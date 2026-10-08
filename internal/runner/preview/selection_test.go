package preview

import (
	"strings"
	"testing"

	allowedext "github.com/qiankunli/case-code-review/internal/config/allowlist"
	"github.com/qiankunli/case-code-review/internal/config/rules"
)

func TestSelectionUsesCapturedTagsAndCallerPrecedence(t *testing.T) {
	for _, name := range []string{"a_test.go", "vendor/a.go", "bundle.MIN.js", "src/normal.go", "package.json", "odd.custom"} {
		tags := allowedext.Classify(name)
		include := &rules.FileFilter{Include: []string{strings.ToLower(name)}}
		if Select(name, tags, false, include) != ExcludeNone {
			t.Fatal("include did not override defaults", name)
		}
		include.Exclude = []string{strings.ToLower(name)}
		if Select(name, tags, false, include) != ExcludeUserRule {
			t.Fatal("exclude did not win", name)
		}
		if Select(name, tags, true, include) != ExcludeBinary {
			t.Fatal("binary did not win", name)
		}
	}
	if Select("a_test.go", nil, false, nil) != ExcludeNone {
		t.Fatal("selection reclassified captured facts")
	}
}
