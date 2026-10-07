package harness

import (
	"context"

	"github.com/compforge/agentgo"
	agentcontext "github.com/compforge/agentgo/context"
	"github.com/compforge/go-stdx/timeline"

	"github.com/qiankunli/case-code-review/internal/harness/compactor"
)

// newContextCompactor wires CCR policy to AgentGo's context engine.
func newContextCompactor(spec ExecutionSpec, model agentgo.ChatModel, window, reserve int) agentcontext.Compactor {
	var stages []compactor.Stage
	if spec.FileEvictEnabled {
		stages = append(stages, compactor.Stage{Name: "message", Compactor: &compactor.MessageCompactor{}})
	}
	stages = append(stages,
		compactor.Stage{Name: "tool_result", Compactor: &compactor.ToolResultCompactor{}},
		compactor.Stage{Name: "light_trim", Compactor: compactor.NewLightTrimCompactor(compactor.LightTrimConfig{})},
		compactor.Stage{Name: "summary", Compactor: compactor.NewSummaryCompactor(compactor.SummaryConfig{
			Model: model, ReserveTokens: reserve,
			SystemPrompt: spec.CompressionSystemPrompt, SummaryPrompt: spec.CompressionPrompt,
			UpdateSummaryPrompt: spec.CompressionUpdatePrompt,
		})},
	)
	return &compactor.ZoneCompactor{KeepRecentTokens: max(window/4, 1), LimitTokens: window - reserve, Stages: stages, Observe: recordZoneCompaction}
}

func recordZoneCompaction(ctx context.Context, report compactor.Report) {
	t, ok := timeline.FromContext(ctx)
	if !ok {
		return
	}
	_, stage := timeline.BeginContext(ctx, t, "context.zones", timeline.WithStartTime(report.Started), timeline.WithAttributes(
		timeline.Attribute{Key: "fixed", Value: report.Fixed},
		timeline.Attribute{Key: "active", Value: report.Active},
		timeline.Attribute{Key: "history", Value: report.History},
		timeline.Attribute{Key: "tokens_before", Value: report.Before},
		timeline.Attribute{Key: "tokens_target", Value: report.Target},
	))
	stage.End(report.Err, timeline.WithEndTime(report.Started.Add(report.Duration)), timeline.WithEndAttributes(
		timeline.Attribute{Key: "tokens_after", Value: report.After},
		timeline.Attribute{Key: "strategies", Value: report.Stages},
		timeline.Attribute{Key: "target_met", Value: report.After <= report.Target},
	))
}
