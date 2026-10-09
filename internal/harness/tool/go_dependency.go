package tool

import "github.com/qiankunli/case-code-review/internal/sourceview"

var ReadGoDependency = Named("read_go_dependency")

type GoDependencyResult = sourceview.DependencySource

func DecodeGoDependencyResult(text string) (GoDependencyResult, bool) {
	return sourceview.DecodeDependencySource(text)
}
