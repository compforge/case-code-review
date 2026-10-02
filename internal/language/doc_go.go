package language

import (
	"context"
	"strings"
)

// goDoc renders leading comment prose at a CodeGraph declaration. It does not
// discover declarations or resolve receiver types itself.
func (a *Analyzer) goDoc(source Source, name string) string {
	definition, ok := a.DefinitionByID(context.Background(), source, SymbolID(source.Path, "", name))
	if !ok {
		return ""
	}
	lines := strings.Split(source.Content, "\n")
	end := definition.Span.Start - 1
	start := end
	for start > 0 {
		line := strings.TrimSpace(lines[start-1])
		if strings.HasPrefix(line, "//") {
			start--
			continue
		}
		if strings.HasSuffix(line, "*/") {
			start--
			for start > 0 && !strings.Contains(lines[start], "/*") {
				start--
			}
		}
		break
	}
	if start == end {
		return ""
	}
	var text []string
	for _, line := range lines[start:end] {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "//")
		line = strings.TrimPrefix(line, "/*")
		line = strings.TrimSuffix(line, "*/")
		line = strings.TrimPrefix(strings.TrimSpace(line), "*")
		text = append(text, strings.TrimSpace(line))
	}
	return summarizeDoc(strings.Join(text, "\n"))
}
