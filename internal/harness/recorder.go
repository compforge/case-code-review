package harness

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/qiankunli/case-code-review/internal/llm"
	"github.com/qiankunli/case-code-review/internal/recording"
)

type recordedCall struct {
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
// Only UI routing, arguments, durations and aggregate usage are retained.
// Native event recording belongs to the local AgentGo Session.
type executionRecorder struct {
	executionID string
	taskType    recording.TaskType
	session     *recording.Review
	scope       recording.Scope

	mu     sync.Mutex
	calls  map[string][]recordedCall
	models []recordedModel
	usage  llm.UsageInfo
}

func newExecutionRecorder(spec ExecutionSpec, executionID string) *executionRecorder {
	taskType := spec.TaskType
	if taskType == "" {
		taskType = recording.MainTask
	}
	return &executionRecorder{
		executionID: executionID,
		taskType:    taskType,
		session:     spec.Session,
		scope:       spec.Scope,
		calls:       make(map[string][]recordedCall),
	}
}

func (r *executionRecorder) beginModel(
	taskType recording.TaskType,
	messages []llm.Message,
) *recording.TaskRecord {
	if r.session == nil {
		return nil
	}
	if taskType == "" {
		taskType = recording.MainTask
	}
	return r.session.GetOrCreateScope(r.scope).AppendExecutionTaskRecord(r.executionID, taskType, messages)
}

func (r *executionRecorder) finishModel(
	response *llm.ChatResponse,
	duration time.Duration,
	trackEvent bool,
) {
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

func (r *executionRecorder) finishTool(id string) {
	r.mu.Lock()
	calls := r.calls[id]
	if len(calls) == 0 {
		r.mu.Unlock()
		return
	}
	if len(calls) == 1 {
		delete(r.calls, id)
	} else {
		r.calls[id] = calls[1:]
	}
	r.mu.Unlock()

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
