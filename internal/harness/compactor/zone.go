// Package compactor owns CCR's context retention and compression policies.
package compactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
)

var ErrBudget = errors.New("context compaction cannot meet token budget")

type Stage struct {
	Name      string
	Compactor agentcontext.Compactor
}

type ZoneStats struct {
	Messages int
	Tokens   int
}

// Report contains estimates, never provider usage or source text. Observe runs
// for failed attempts too; only a successful Compact result may be committed.
type Report struct {
	Started                time.Time
	Duration               time.Duration
	Fixed, Active, History ZoneStats
	Before, After, Target  int
	Stages                 []string
	Err                    error
}

// ZoneCompactor protects instructions and complete recent rounds around every
// history strategy. Priority orders reductions inside history, not protection.
type ZoneCompactor struct {
	KeepRecentTokens int
	// LimitTokens bounds explicit/overflow compaction, where expect is zero.
	LimitTokens int
	Stages      []Stage
	Observe     func(context.Context, Report)
}

func (c *ZoneCompactor) Compact(ctx context.Context, messages []agentgo.AgentMessage, expect float64) (result []agentgo.AgentMessage, err error) {
	before := agentcontext.EstimateTotal(messages)
	if expect >= 1 || len(messages) == 0 {
		return messages, nil
	}
	report := Report{Started: time.Now(), Before: before, After: before, Target: int(float64(before) * max(0, expect))}
	defer func() {
		report.Duration, report.Err = time.Since(report.Started), err
		if c.Observe != nil {
			c.Observe(ctx, report)
		}
	}()
	if err = validateTools(messages); err != nil {
		return nil, err
	}
	parts := partition(messages, c.KeepRecentTokens)
	for _, part := range parts {
		stats := &report.History
		if part.zone == fixed {
			stats = &report.Fixed
		}
		if part.zone == active {
			stats = &report.Active
		}
		stats.Messages += len(part.messages)
		stats.Tokens += agentcontext.EstimateTotal(part.messages)
	}
	protected := report.Fixed.Tokens + report.Active.Tokens
	// expect=0 is AgentGo's explicit/manual strongest-reduction request, not a
	// demand for an empty prompt. Automatic requests must meet their budget.
	forced := expect <= 0
	if forced {
		report.Target = before
		if c.LimitTokens > 0 {
			report.Target = c.LimitTokens
		}
	}
	if protected > report.Target || (protected == report.Target && report.History.Tokens > 0) {
		return nil, fmt.Errorf("%w: protected=%d target=%d", ErrBudget, protected, report.Target)
	}
	historyTarget := max(0, report.Target-protected)
	remaining := report.History.Tokens
	for _, stage := range c.Stages {
		if !forced && remaining <= historyTarget {
			break
		}
		used := false
		for i := range parts {
			part := &parts[i]
			if part.zone != history || len(part.messages) == 0 {
				continue
			}
			if !forced && remaining <= historyTarget {
				break
			}
			tokens := agentcontext.EstimateTotal(part.messages)
			ratio := 0.0
			if !forced && tokens > 0 {
				ratio = float64(max(0, tokens-(remaining-historyTarget))) / float64(tokens)
			}
			if !used {
				report.Stages = append(report.Stages, stage.Name)
				used = true
			}
			next, stageErr := stage.Compactor.Compact(ctx, part.messages, ratio)
			if stageErr != nil {
				return nil, fmt.Errorf("history %s: %w", stage.Name, stageErr)
			}
			after := agentcontext.EstimateTotal(next)
			if after < tokens {
				part.messages = next
				remaining -= tokens - after
			}
		}
	}
	result = flatten(parts)
	report.After = agentcontext.EstimateTotal(result)
	if err = validateTools(result); err != nil {
		return nil, err
	}
	if report.After > report.Target {
		return nil, fmt.Errorf("%w: after=%d target=%d", ErrBudget, report.After, report.Target)
	}
	return result, nil
}
