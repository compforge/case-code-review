package llm

import (
	"errors"
	"fmt"
	"strings"
)

// ErrOutputTruncated applies to consumers that require a complete text answer.
// Provider calls still return partial responses and usage without a transport error.
var ErrOutputTruncated = errors.New("model output reached the token limit")

// CompleteText rejects truncated or reasoning-only output for single-shot tasks.
// Agent loops consume the original response so they can decide whether to continue.
func (r *ChatResponse) CompleteText() (string, error) {
	if r == nil || len(r.Choices) == 0 {
		return "", fmt.Errorf("model returned no choices")
	}
	switch strings.ToLower(r.Choices[0].FinishReason) {
	case "length", "max_tokens":
		return "", ErrOutputTruncated
	}
	text := r.VisibleContent()
	if text == "" {
		return "", fmt.Errorf("model returned no visible text")
	}
	return text, nil
}
