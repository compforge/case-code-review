package llm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/compforge/agentgo"
)

var ErrOutputTruncated = errors.New("model output reached the token limit")

// CompleteText is CCR's completion policy for tasks requiring a whole visible answer.
func CompleteText(response *agentgo.LLMResponse) (string, error) {
	if response == nil {
		return "", fmt.Errorf("model returned no response")
	}
	if response.Message.StopReason == agentgo.StopReasonLength {
		return "", ErrOutputTruncated
	}
	text := strings.TrimSpace(stripThinkTags(response.Message.TextContent()))
	if text == "" {
		return "", fmt.Errorf("model returned no visible text")
	}
	return text, nil
}
