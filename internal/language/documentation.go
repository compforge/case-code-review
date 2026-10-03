package language

import (
	"strings"

	cg "github.com/compforge/codegraph"
)

// CodeGraph owns attachment and identity. CCR only renders the retained fragments.
func declarationDoc(n cg.Node) string {
	var parts []string
	for _, doc := range n.Documentation {
		text := strings.TrimSpace(doc.Text)
		if n.Language == "python" {
			text = strings.TrimLeft(text, "rRuU")
			for _, q := range []string{`"""`, "'''", `"`, "'"} {
				if strings.HasPrefix(text, q) && strings.HasSuffix(text, q) {
					text = strings.TrimSuffix(strings.TrimPrefix(text, q), q)
					break
				}
			}
		} else {
			var lines []string
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				line = strings.TrimPrefix(line, "//")
				line = strings.TrimPrefix(line, "/*")
				line = strings.TrimSuffix(line, "*/")
				line = strings.TrimPrefix(strings.TrimSpace(line), "*")
				lines = append(lines, strings.TrimSpace(line))
			}
			text = strings.Join(lines, "\n")
		}
		parts = append(parts, text)
	}
	return summarizeDoc(strings.Join(parts, "\n"))
}
func summarizeDoc(doc string) string {
	doc = strings.TrimSpace(doc)
	if i := strings.Index(doc, "\n\n"); i >= 0 {
		doc = doc[:i]
	}
	return strings.Join(strings.Fields(doc), " ")
}
