package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/llm"
)

// chatModel keeps CCR's provider routing and retry behavior behind agentgo's
// model contract. The execution kernel must not know which concrete provider
// or LLMRouter served a request.
type chatModel struct {
	client    llm.LLMClient
	model     string
	maxTokens int
	recorder  *executionRecorder
	taskType  session.TaskType
	events    bool
}

func (m *chatModel) Generate(
	ctx context.Context,
	messages []agentgo.Message,
	tools []agentgo.ToolSpec,
	opts ...agentgo.CallOption,
) (*agentgo.LLMResponse, error) {
	cfg := agentgo.ResolveCallConfig(opts)
	maxTokens := cfg.MaxTokens
	if maxTokens == 0 {
		maxTokens = m.maxTokens
	}

	request := llm.ChatRequest{
		Model:     m.model,
		Messages:  toLLMMessages(messages),
		Tools:     toLLMToolDefs(tools),
		MaxTokens: maxTokens,
	}
	record := m.recorder.beginModel(m.taskType, request.Messages)
	started := time.Now()
	resp, err := m.client.CompletionsWithCtx(ctx, request)
	m.recorder.finishModel(record, resp, err, time.Since(started), m.events)
	if err != nil {
		return nil, err
	}
	message, err := toAgentGoResponse(resp)
	if err != nil {
		return nil, err
	}
	return &agentgo.LLMResponse{Message: message}, nil
}

// CCR's current clients are non-streaming. A one-event stream preserves
// agentgo's execution contract while provider streaming remains a separate
// concern from adopting the loop runtime.
func (m *chatModel) GenerateStream(
	ctx context.Context,
	messages []agentgo.Message,
	tools []agentgo.ToolSpec,
	opts ...agentgo.CallOption,
) (<-chan agentgo.StreamEvent, error) {
	events := make(chan agentgo.StreamEvent, 1)
	go func() {
		defer close(events)
		resp, err := m.Generate(ctx, messages, tools, opts...)
		if err != nil {
			events <- agentgo.StreamEvent{Type: agentgo.StreamEventError, Err: err}
			return
		}
		events <- agentgo.StreamEvent{
			Type:       agentgo.StreamEventDone,
			Message:    resp.Message,
			StopReason: resp.Message.StopReason,
		}
	}()
	return events, nil
}

func (m *chatModel) SupportsTools() bool { return true }
func (m *chatModel) ModelName() string   { return m.model }

func toLLMMessages(messages []agentgo.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, message := range messages {
		switch message.Role {
		case agentgo.RoleAssistant:
			calls := message.ToolCalls()
			toolCalls := make([]llm.ToolCall, 0, len(calls))
			for _, call := range calls {
				toolCalls = append(toolCalls, llm.ToolCall{
					ID:   call.ID,
					Type: "function",
					Function: llm.FunctionCall{
						Name:      call.Name,
						Arguments: string(call.Args),
					},
				})
			}
			out = append(out, llm.NewToolCallMessage(message.TextContent(), toolCalls))
		case agentgo.RoleTool:
			out = append(out, llm.NewToolResultMessage(
				metadataString(message.Metadata, "tool_call_id"),
				message.TextContent(),
			))
		default:
			out = append(out, llm.NewTextMessage(string(message.Role), message.TextContent()))
		}
	}
	return out
}

func toLLMToolDefs(tools []agentgo.ToolSpec) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(tools))
	for _, tool := range tools {
		parameters, _ := tool.Parameters.(map[string]any)
		out = append(out, llm.ToolDef{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  parameters,
			},
		})
	}
	return out
}

func toAgentGoResponse(resp *llm.ChatResponse) (agentgo.Message, error) {
	if resp == nil || len(resp.Choices) == 0 {
		return agentgo.Message{}, fmt.Errorf("harness: LLM returned no choices")
	}

	choice := resp.Choices[0]
	content := make([]agentgo.ContentBlock, 0, len(choice.Message.ToolCalls)+1)
	if text := resp.Content(); text != "" {
		content = append(content, agentgo.TextBlock(text))
	}
	for _, call := range choice.Message.ToolCalls {
		args := json.RawMessage(call.Function.Arguments)
		toolCall := agentgo.ToolCall{
			ID:   call.ID,
			Name: call.Function.Name,
			Args: args,
		}
		if !json.Valid(args) {
			toolCall.Args = json.RawMessage(`{}`)
			toolCall.ArgsInvalid = true
			toolCall.ArgsRawText = call.Function.Arguments
			toolCall.ArgsParseError = "invalid JSON tool arguments"
		}
		content = append(content, agentgo.ToolCallBlock(toolCall))
	}

	message := agentgo.Message{
		Role:       agentgo.RoleAssistant,
		Content:    content,
		StopReason: toStopReason(choice.FinishReason, len(choice.Message.ToolCalls) > 0),
		Timestamp:  time.Now(),
	}
	if resp.Usage != nil {
		message.Usage = &agentgo.Usage{
			Provider:    resp.Alias,
			Model:       resp.Model,
			Input:       int(resp.Usage.PromptTokens),
			Output:      int(resp.Usage.CompletionTokens),
			CacheRead:   int(resp.Usage.CacheReadTokens),
			CacheWrite:  int(resp.Usage.CacheWriteTokens),
			TotalTokens: int(resp.Usage.TotalTokens),
		}
	}
	return message, nil
}

func toStopReason(finishReason string, hasToolCalls bool) agentgo.StopReason {
	switch strings.ToLower(finishReason) {
	case "length", "max_tokens":
		return agentgo.StopReasonLength
	case "error":
		return agentgo.StopReasonError
	case "content_filter", "safety":
		return agentgo.StopReasonSafety
	case "tool_calls", "tool_use":
		return agentgo.StopReasonToolUse
	}
	if hasToolCalls {
		return agentgo.StopReasonToolUse
	}
	return agentgo.StopReasonStop
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}
