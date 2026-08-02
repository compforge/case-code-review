package unitreview

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/qiankunli/case-code-review/internal/config/template"
	"github.com/qiankunli/case-code-review/internal/harness"
	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/runner/finding"
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// HypothesisHook turns the terminal submit_hypotheses call into Runner-owned
// investigative results. Finding conversion remains deferred until Trial.
type HypothesisHook struct {
	WorkerPool   *harness.WorkerPool
	Session      *session.SessionHistory
	ChangeLookup func(path string) *change.Change
	LLMClient    llm.LLMClient
	Template     template.Template
	Model        string
	Relocation   bool
	RecordUsage  func(*llm.UsageInfo)
	// OnResolved runs after anchoring/relocation, when the
	// Hypothesis is stable enough to enter downstream Lane assignment.
	OnResolved func(Hypothesis)
}

var _ harness.ToolHandler = (*HypothesisHook)(nil)

func (h *HypothesisHook) HandleTool(
	ctx context.Context,
	call harness.ToolRequest,
) (tool.TaskCheckpoint, bool) {
	if call.Tool != SubmitHypotheses {
		return tool.TaskCheckpoint{}, false
	}

	started := time.Now()
	telemetry.PrintToolCallStarted(call.Tool.Name(), call.Args)
	hypotheses, errMsg := ParseHypotheses(call.Args)
	if errMsg != "" {
		telemetry.RecordToolCall(ctx, call.Tool.Name(), time.Since(started), false)
		return tool.Of(errMsg), true
	}
	for i := range hypotheses {
		if !slices.Contains(call.Scope.Paths, hypotheses[i].Path) {
			hypotheses[i].Path = call.Scope.Path()
		}
		hypotheses[i].Alias = call.Alias
		hypotheses[i].OriginUnit = call.Scope.ID
	}

	resolveAndCollect := func(workCtx context.Context) {
		for i := range hypotheses {
			hypothesis := &hypotheses[i]
			draft := FindingFor(*hypothesis)
			var ch *change.Change
			if h.ChangeLookup != nil {
				ch = h.ChangeLookup(hypothesis.Path)
			}
			if ch != nil && !finding.ResolveComment(&draft, ch) &&
				h.Relocation && h.Template.ReLocationTask != nil {
				h.relocate(workCtx, call.Scope, &draft, ch)
			}
			hypothesis.StartLine = draft.StartLine
			hypothesis.EndLine = draft.EndLine
			hypothesis.ExistingCode = draft.ExistingCode
			hypothesis.Fingerprint = FingerprintFor(*hypothesis)
			hypothesis.ID = IDFor(*hypothesis)
			if h.OnResolved != nil {
				h.OnResolved(*hypothesis)
			}
		}
	}

	if h.WorkerPool != nil {
		asyncCtx := context.WithoutCancel(ctx)
		var scope *session.ScopeSession
		if h.Session != nil {
			scope = h.Session.GetOrCreateScope(call.Scope)
			scope.BeginAsync()
		}
		h.WorkerPool.Submit(func() error {
			if scope != nil {
				defer scope.EndAsync()
			}
			resolveAndCollect(asyncCtx)
			telemetry.PrintToolCallFinished(call.Tool.Name(), time.Since(started))
			return nil
		})
		telemetry.RecordToolCall(asyncCtx, call.Tool.Name(), time.Since(started), true)
		return tool.CompleteWith(HypothesesSubmitted), true
	}

	resolveAndCollect(ctx)
	duration := time.Since(started)
	telemetry.RecordToolCall(ctx, call.Tool.Name(), duration, true)
	telemetry.PrintToolCallFinished(call.Tool.Name(), duration)
	return tool.CompleteWith(HypothesesSubmitted), true
}

func (h *HypothesisHook) relocate(
	ctx context.Context,
	scope session.Scope,
	draft *finding.Finding,
	ch *change.Change,
) {
	started := time.Now()
	_, response, messages := finding.ReLocateComment(
		ctx, draft, ch, h.LLMClient, h.Template.ReLocationTask, h.Model, h.Template.MaxTokens,
	)
	if messages == nil || h.Session == nil {
		if response != nil && h.RecordUsage != nil {
			h.RecordUsage(response.Usage)
		}
		return
	}

	record := h.Session.GetOrCreateScope(scope).AppendTaskRecord(session.ReLocationTask, messages)
	if response == nil {
		record.SetError(fmt.Errorf("re-location LLM call failed"), time.Since(started))
		return
	}
	record.SetResponse(response, time.Since(started))
	if h.RecordUsage != nil {
		h.RecordUsage(response.Usage)
	}
}
