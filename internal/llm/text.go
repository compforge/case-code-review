package llm

import "strings"

// CountMessagesTokens returns the rough token count of messages by summing
// their text content.
func CountMessagesTokens(messages []Message) int {
	var total int
	for _, message := range messages {
		total += CountTokens(message.ExtractText())
	}
	return total
}

// StripMarkdownFences removes Markdown code fences some models add around
// structured or otherwise machine-consumed output.
func StripMarkdownFences(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = text[newline+1:]
		} else {
			text = strings.TrimPrefix(text, "```json")
			text = strings.TrimPrefix(text, "```")
		}
	}
	text = strings.TrimSpace(text)
	if strings.HasSuffix(text, "```") {
		text = strings.TrimSuffix(text, "```")
		text = strings.TrimSpace(text)
	}
	return text
}
