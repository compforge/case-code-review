package llm

import (
	"errors"
	"fmt"
	"maps"
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

// reasoningExtra merges one call's explicit effort after provider defaults.
// Supported effort values and aliases belong to the selected model; pass them
// through instead of collapsing them to a universal low/high scale.
// +spec=`An explicit off override cannot leave a raw thinking-enabled default in the request or mutate the next call's defaults`
func reasoningExtra(defaults map[string]any, effort string, anthropic bool) map[string]any {
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort == "" || effort == "auto" {
		return defaults
	}
	if effort == "off" {
		effort = "none"
	}
	extra := maps.Clone(defaults)
	if extra == nil {
		extra = make(map[string]any)
	}
	delete(extra, "reasoning")
	delete(extra, "thinking_budget")
	if anthropic {
		delete(extra, "reasoning_effort")
		output, _ := extra["output_config"].(map[string]any)
		output = maps.Clone(output)
		if output == nil {
			output = make(map[string]any)
		}
		if effort == "none" {
			extra["thinking"] = map[string]any{"type": "disabled"}
			delete(output, "effort")
		} else {
			extra["thinking"] = map[string]any{"type": "adaptive"}
			output["effort"] = effort
		}
		if len(output) > 0 {
			extra["output_config"] = output
		} else {
			delete(extra, "output_config")
		}
	} else {
		extra["reasoning_effort"] = effort
		// OpenAI itself has no thinking object. Override this vendor extension only
		// when the selected endpoint already declares it in its defaults (e.g. Ark).
		if _, ok := extra["thinking"]; ok {
			mode := "enabled"
			if effort == "none" {
				mode = "disabled"
			}
			extra["thinking"] = map[string]any{"type": mode}
		}
	}
	if _, ok := extra["enable_thinking"]; ok {
		extra["enable_thinking"] = effort != "none"
	}
	return extra
}
