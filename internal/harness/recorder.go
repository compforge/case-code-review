package harness

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
)

type recordedCall struct {
	record    *session.TaskRecord
	alias     string
	arguments string
	duration  time.Duration
}

type recordedModel struct {
	alias    string
	model    string
	duration time.Duration
}

// executionRecorder joins model responses and later tool events by call ID.
// A model turn owns its session TaskRecord; tools only append results to the
// record of the assistant message that requested them.
type executionRecorder struct {
	executionID string
	taskType    session.TaskType
	session     *session.SessionHistory
	scope       session.Scope

	mu           sync.Mutex
	calls        map[string][]recordedCall
	models       []recordedModel
	usage        llm.UsageInfo
	projectionNo int
}

func newExecutionRecorder(spec ExecutionSpec, executionID string) *executionRecorder {
	taskType := spec.TaskType
	if taskType == "" {
		taskType = session.MainTask
	}
	return &executionRecorder{
		executionID: executionID,
		taskType:    taskType,
		session:     spec.Session,
		scope:       spec.Scope,
		calls:       make(map[string][]recordedCall),
	}
}

func (r *executionRecorder) recordCompaction(compaction session.ContextCompaction) {
	if r.session == nil {
		return
	}
	r.session.WriteContextCompaction(r.scope, r.executionID, r.taskType, compaction)
}

func (r *executionRecorder) recordContextProjected(items []agentgo.ContextItem) {
	if r.session == nil {
		return
	}
	r.mu.Lock()
	r.projectionNo++
	projectionNo := r.projectionNo
	r.mu.Unlock()
	r.session.WriteContextProjected(r.scope, r.executionID, r.taskType, projectionNo, items)
}

func (r *executionRecorder) beginModel(
	taskType session.TaskType,
	messages []llm.Message,
) *session.TaskRecord {
	if r.session == nil {
		return nil
	}
	if taskType == "" {
		taskType = session.MainTask
	}
	return r.session.GetOrCreateScope(r.scope).AppendExecutionTaskRecord(r.executionID, taskType, messages)
}

func (r *executionRecorder) finishExecution(taskType session.TaskType, result ExecutionResult, duration time.Duration) {
	if r.session == nil {
		return
	}
	r.session.WriteExecutionEnd(r.scope, session.ExecutionEnd{
		ID: r.executionID, TaskType: taskType,
		Outcome: result.State, Reason: result.Reason,
		Turns: result.Turns, ToolCalls: result.ToolCalls, ToolErrors: result.ToolErrors,
		Duration: duration,
	})
}

func (r *executionRecorder) finishModel(
	record *session.TaskRecord,
	response *llm.ChatResponse,
	err error,
	duration time.Duration,
	trackEvent bool,
) {
	if record != nil {
		if err != nil {
			record.SetError(err, duration)
		} else {
			record.SetResponse(response, duration)
		}
	}
	if response == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if response.Usage != nil {
		addLLMUsage(&r.usage, response.Usage)
	}
	if trackEvent {
		r.models = append(r.models, recordedModel{
			alias:    response.Alias,
			model:    response.Model,
			duration: duration,
		})
	}
	for _, call := range response.ToolCalls() {
		r.calls[call.ID] = append(r.calls[call.ID], recordedCall{
			record:    record,
			alias:     response.Alias,
			arguments: call.Function.Arguments,
		})
	}
}

func (r *executionRecorder) Usage() llm.UsageInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.usage
}

func (r *executionRecorder) call(id string) recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls[id]
	if len(calls) == 0 {
		return recordedCall{}
	}
	return calls[0]
}

func (r *executionRecorder) nextModel() recordedModel {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.models) == 0 {
		return recordedModel{}
	}
	model := r.models[0]
	r.models = r.models[1:]
	return model
}

func (r *executionRecorder) finishToolExecution(id string, duration time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	calls := r.calls[id]
	for i := range calls {
		if calls[i].duration == 0 {
			calls[i].duration = duration
			r.calls[id] = calls
			return
		}
	}
}

func (r *executionRecorder) finishTool(id, name string, result []byte, isError bool) {
	r.mu.Lock()
	calls := r.calls[id]
	if len(calls) == 0 {
		r.mu.Unlock()
		return
	}
	call := calls[0]
	if len(calls) == 1 {
		delete(r.calls, id)
	} else {
		r.calls[id] = calls[1:]
	}
	r.mu.Unlock()

	if call.record == nil {
		return
	}
	call.record.AddToolResultWithMetadata(id, name, call.arguments, toolResultText(result), !isError, call.duration, nil)
}

func toolResultText(raw []byte) string {
	var text string
	if len(raw) > 0 && json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func addLLMUsage(total *llm.UsageInfo, usage *llm.UsageInfo) {
	total.PromptTokens += usage.PromptTokens
	total.CompletionTokens += usage.CompletionTokens
	total.CacheReadTokens += usage.CacheReadTokens
	total.CacheWriteTokens += usage.CacheWriteTokens
	total.TotalTokens += usage.TotalTokens
}
