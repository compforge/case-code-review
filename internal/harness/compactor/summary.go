package compactor

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

// SummaryConfig configures historical checkpoints. There is deliberately no
// recent-tail or split-turn option: the zone boundary owns that decision.
type SummaryConfig struct {
	Model               agentgo.ChatModel
	ReserveTokens       int
	SystemPrompt        string
	SummaryPrompt       string
	UpdateSummaryPrompt string
}

type SummaryCompactor struct {
	cfg      SummaryConfig
	sequence atomic.Uint64
}

func NewSummaryCompactor(cfg SummaryConfig) *SummaryCompactor {
	return &SummaryCompactor{cfg: cfg}
}

// Compact summarizes the entire supplied history segment. AgentGo's checkpoint
// and model execution contracts preserve raw evidence, usage and loop events.
func (s *SummaryCompactor) Compact(ctx context.Context, messages []agentgo.AgentMessage, expect float64) ([]agentgo.AgentMessage, error) {
	if len(messages) == 0 || expect >= 1 || s.cfg.Model == nil {
		return messages, nil
	}
	before := agentcontext.EstimateTotal(messages)
	reserve := s.cfg.ReserveTokens
	if reserve <= 0 {
		reserve = max(1, before-int(float64(before)*clampRatio(expect)))
	}
	if expect <= 0 {
		reserve = 1024
	}
	source := rawSummaryView(messages)
	var previous []string
	for _, message := range source {
		if summary, ok := message.(agentcontext.ContextSummary); ok {
			previous = append(previous, summary.Summary)
		}
	}
	prompts := summaryPrompts{System: s.cfg.SystemPrompt, Summary: s.cfg.SummaryPrompt, Update: s.cfg.UpdateSummaryPrompt}
	// A partition can have several history segments under the same parent.
	// Give each call its own coordinate so usage and retries cannot collide.
	execution := summaryExecution(ctx, fmt.Sprintf("summary-history-%d", s.sequence.Add(1)))
	summary, err := generateSummary(ctx, s.cfg.Model, execution, prompts,
		stripImageBlocks(source), strings.Join(previous, "\n\n"),
		agentgo.WithMaxTokens(max(1, int(float64(reserve)*0.8))), agentgo.WithThinking(agentgo.ThinkingOff))
	if err != nil {
		return nil, err
	}
	raw := sourceMessages(source)
	readFiles, modifiedFiles := extractFileOps(raw)
	return []agentgo.AgentMessage{agentcontext.ContextSummary{
		Summary:      summary + formatFileOps(readFiles, modifiedFiles),
		TokensBefore: before, ReadFiles: readFiles, ModifiedFiles: modifiedFiles,
		RawMessages: raw, Compacted: len(messages), Timestamp: time.Now(),
	}}, nil
}
