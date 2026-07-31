package finding

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
	"github.com/qiankunli/case-code-review/internal/telemetry"
	"github.com/qiankunli/case-code-review/internal/unit/change"
)

// Hook turns the generic code_comment tool call into Runner findings. Line
// resolution, optional re-location and result storage stay outside Harness.
type Hook struct {
	Collector    *Collector
	WorkerPool   *harness.WorkerPool
	Session      *session.SessionHistory
	ChangeLookup func(path string) *change.Change
	LLMClient    llm.LLMClient
	Template     template.Template
	Model        string
	Relocation   bool
	RecordUsage  func(*llm.UsageInfo)
}

var _ harness.ToolHandler = (*Hook)(nil)

// HandleTool implements harness.ToolHandler.
func (h *Hook) HandleTool(ctx context.Context, call harness.ToolRequest) (tool.TaskCheckpoint, bool) {
	return h.handleTool(ctx, call)
}

func (h *Hook) handleTool(
	ctx context.Context,
	call harness.ToolRequest,
) (tool.TaskCheckpoint, bool) {
	if call.Tool != CodeComment {
		return tool.TaskCheckpoint{}, false
	}

	path := call.Scope.Path()
	if path != "" {
		reported, _ := call.Args["path"].(string)
		if reported == "" || !slices.Contains(call.Scope.Paths, reported) {
			call.Args["path"] = path
		}
	}

	started := time.Now()
	telemetry.PrintToolCallStarted(call.Tool.Name(), call.Args)
	comments, errMsg := ParseComments(call.Args)
	if errMsg != "" {
		telemetry.RecordToolCall(ctx, call.Tool.Name(), time.Since(started), false)
		return tool.Of(errMsg), true
	}
	for i := range comments {
		comments[i].Alias = call.Alias
	}

	resolveAndCollect := func(workCtx context.Context) {
		for i := range comments {
			cm := &comments[i]
			var ch *change.Change
			if h.ChangeLookup != nil {
				ch = h.ChangeLookup(cm.Path)
			}
			if ch != nil && !ResolveComment(cm, ch) && h.Relocation && h.Template.ReLocationTask != nil {
				h.relocate(workCtx, call.Scope, cm, ch)
			}
			if h.Collector != nil {
				h.Collector.Add(*cm)
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
		return tool.Of(submitSucceeded), true
	}

	resolveAndCollect(ctx)
	duration := time.Since(started)
	telemetry.RecordToolCall(ctx, call.Tool.Name(), duration, true)
	telemetry.PrintToolCallFinished(call.Tool.Name(), duration)
	return tool.Of(submitSucceeded), true
}

func (h *Hook) relocate(ctx context.Context, scope session.Scope, finding *Finding, ch *change.Change) {
	started := time.Now()
	_, response, messages := ReLocateComment(
		ctx, finding, ch, h.LLMClient, h.Template.ReLocationTask, h.Model, h.Template.MaxTokens,
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
