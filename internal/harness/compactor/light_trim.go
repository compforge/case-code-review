// Derived from compforge/agentgo context compactors (Apache-2.0).
// CCR owns history-only policy; ZoneCompactor owns all suffix protection.
package compactor

import (
	"context"
	"fmt"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

// LightTrimConfig controls text projection within the history zone.
type LightTrimConfig struct {
	TextThreshold int
	PreserveHead  int
	PreserveTail  int
}

type LightTrimCompactor struct {
	cfg LightTrimConfig
}

func NewLightTrimCompactor(cfg LightTrimConfig) *LightTrimCompactor {
	if cfg.TextThreshold <= 0 {
		cfg.TextThreshold = 4000
	}
	if cfg.PreserveHead <= 0 {
		cfg.PreserveHead = 1200
	}
	if cfg.PreserveTail <= 0 {
		cfg.PreserveTail = 800
	}
	return &LightTrimCompactor{cfg: cfg}
}

func (s *LightTrimCompactor) Compact(_ context.Context, input agentgo.TransformContext, expect float64) ([]agentgo.AgentMessage, error) {
	messages := input.Messages
	if len(messages) == 0 || expect >= 1 {
		return messages, nil
	}

	out := copyMessages(messages)
	tokens := agentcontext.EstimateTotal(out)
	target := int(float64(tokens) * clampRatio(expect))

	for i := 0; i < len(out); i++ {
		if tokens <= target {
			break
		}
		msg, ok := out[i].ToMessage()
		if !ok {
			continue
		}
		before := agentcontext.EstimateTokens(out[i])
		next, changed := trimLongTextBlocks(msg, s.cfg.TextThreshold, s.cfg.PreserveHead, s.cfg.PreserveTail)
		if !changed {
			continue
		}
		after := agentcontext.EstimateTokens(next)
		if after < before {
			out[i] = newProjectedMessage(out[i], next)
			tokens -= before - after
		}
	}

	return out, nil
}

func trimLongTextBlocks(msg agentgo.Message, threshold, preserveHead, preserveTail int) (agentgo.Message, bool) {
	newContent := make([]agentgo.ContentBlock, len(msg.Content))
	changed := false
	trimmedBlocks := 0
	for i, block := range msg.Content {
		if block.Type != agentgo.ContentText {
			newContent[i] = block
			continue
		}
		runes := []rune(block.Text)
		if len(runes) <= threshold {
			newContent[i] = block
			continue
		}
		headCount := min(preserveHead, len(runes))
		tailCount := min(preserveTail, len(runes)-headCount)
		head := string(runes[:headCount])
		tail := string(runes[len(runes)-tailCount:])
		trimmed := len(runes) - headCount - tailCount
		newContent[i] = agentgo.ContentBlock{
			Type: agentgo.ContentText,
			Text: fmt.Sprintf("%s\n[%d characters trimmed]\n%s", head, trimmed, tail),
		}
		changed = true
		trimmedBlocks++
	}
	if !changed {
		return msg, false
	}
	next := msg
	next.Content = newContent
	next.Metadata = cloneMetadata(msg.Metadata)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["trimmed_text_blocks"] = trimmedBlocks
	return next, true
}
