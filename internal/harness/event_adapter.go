package harness

import (
	"encoding/json"

	"github.com/voocel/agentcore"

	"github.com/qiankunli/case-code-review/internal/llm"
)

func emitExecutionEvent(sink EventSink, recorder *executionRecorder, event agentcore.Event) {
	if sink == nil {
		return
	}

	out := ExecutionEvent{}
	switch event.Type {
	case agentcore.EventModelResponse:
		out.Type = EventModelResponse
		recorded := recorder.nextModel()
		out.Alias = recorded.alias
		out.Model = recorded.model
		out.Duration = recorded.duration
		if message, ok := event.Message.(agentcore.Message); ok {
			wire := toLLMMessages([]agentcore.Message{message})
			if len(wire) == 1 {
				out.Message = &wire[0]
			}
			out.Usage = usageInfo(message.Usage)
		}
	case agentcore.EventToolExecStart:
		out.Type = EventToolStart
		out.ToolCallID = event.ToolID
		out.Tool = event.Tool
		out.Arguments = cloneRaw(event.Args)
	case agentcore.EventToolExecEnd:
		out.Type = EventToolEnd
		out.ToolCallID = event.ToolID
		out.Tool = event.Tool
		out.Result = cloneRaw(event.Result)
		out.IsError = event.IsError
		recorded := recorder.call(event.ToolID)
		out.Arguments = json.RawMessage(recorded.arguments)
		out.Duration = recorded.duration
	case agentcore.EventError:
		out.Type = EventExecutionError
		out.Err = event.Err
	case agentcore.EventAgentEnd:
		out.Type = EventExecutionEnd
		if event.Summary != nil {
			out.EndReason = string(event.Summary.EndReason)
		}
	default:
		return
	}
	sink.OnExecutionEvent(out)
}

func usageInfo(usage *agentcore.Usage) *llm.UsageInfo {
	if usage == nil {
		return nil
	}
	return &llm.UsageInfo{
		PromptTokens:     int64(usage.Input),
		CompletionTokens: int64(usage.Output),
		CacheReadTokens:  int64(usage.CacheRead),
		CacheWriteTokens: int64(usage.CacheWrite),
		TotalTokens:      int64(usage.TotalTokens),
	}
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}
