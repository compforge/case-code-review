package tool

import (
	"fmt"
	"unicode/utf8"
)

// MaxResultBytes bounds one tool result admitted to the model context. This is
// a byte limit, not a token estimate; it also bounds minified and generated data.
const MaxResultBytes = 32 * 1024

// LimitResult marks incomplete output explicitly and never splits a UTF-8 rune.
func LimitResult(content string, limit int) string {
	if len(content) <= limit {
		return content
	}
	note := fmt.Sprintf("\n[Output truncated: original %d bytes; limit %d bytes. Narrow the query or request a smaller source range.]\n", len(content), limit)
	if len(note) >= limit {
		return utf8Prefix(note, limit)
	}
	return utf8Prefix(content, limit-len(note)) + note
}

func utf8Prefix(content string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(content) <= limit {
		return content
	}
	for limit > 0 && !utf8.RuneStart(content[limit]) {
		limit--
	}
	return content[:limit]
}
