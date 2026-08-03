package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/compforge/agentgo"

	"github.com/qiankunli/case-code-review/internal/harness/session"
	"github.com/qiankunli/case-code-review/internal/harness/tool"
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
		rawArgs := json.RawMessage(call.Function.Arguments)
		args := canonicalToolArguments(call.Function.Name, rawArgs)
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

// canonicalToolArguments repairs only lossless, CCR-owned schema drift before
// AgentGo validates the call. Empty, malformed, or semantically incomplete
// arguments remain errors for the model to correct on its next turn.
func canonicalToolArguments(name string, raw json.RawMessage) json.RawMessage {
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return raw
	}

	switch name {
	case tool.FileRead.Name(), tool.FileReadBase.Name():
		canonicalizeReadArguments(args)
	case tool.CodeSearch.Name():
		canonicalizeSearchArguments(args)
	default:
		return raw
	}

	canonical, err := json.Marshal(args)
	if err != nil {
		return raw
	}
	return canonical
}

func canonicalizeReadArguments(args map[string]any) {
	if reads, ok := args["reads"]; ok {
		switch reads := reads.(type) {
		case map[string]any:
			args["reads"] = []any{reads}
		}
		return
	}
	if _, ok := args["file_path"]; ok {
		item := maps.Clone(args)
		clear(args)
		args["reads"] = []any{item}
	}
}
func canonicalizeSearchArguments(args map[string]any) {
	if searches, ok := args["searches"]; ok {
		switch searches := searches.(type) {
		case map[string]any:
			canonicalizeSearchItem(searches)
			args["searches"] = []any{searches}
		case []any:
			for _, value := range searches {
				if item, ok := value.(map[string]any); ok {
					canonicalizeSearchItem(item)
				}
			}
		}
		return
	}

	if queries, ok := args["queries"].([]any); ok && len(queries) > 0 {
		shared := searchSharedArguments(args)
		searches := make([]any, 0, len(queries))
		for _, value := range queries {
			switch value := value.(type) {
			case string:
				item := maps.Clone(shared)
				item["query"] = value
				canonicalizeSearchItem(item)
				searches = append(searches, item)
			case map[string]any:
				item := maps.Clone(shared)
				maps.Copy(item, value)
				canonicalizeSearchItem(item)
				searches = append(searches, item)
			default:
				return
			}
		}
		clear(args)
		args["searches"] = searches
		return
	}

	canonicalizeSearchItem(args)
	if _, ok := args["query"]; ok {
		item := maps.Clone(args)
		clear(args)
		args["searches"] = []any{item}
	}
}

func canonicalizeSearchItem(item map[string]any) {
	if _, exists := item["query"]; !exists {
		if pattern, ok := item["pattern"].(string); ok && safePatternAlias(pattern, item["syntax"]) {
			item["query"] = pattern
			delete(item, "pattern")
		}
	}
	if syntax, ok := item["syntax"].(string); ok {
		switch strings.ToLower(strings.TrimSpace(syntax)) {
		case "regex", "regexp", "perl":
			item["use_perl_regexp"] = true
			delete(item, "syntax")
		case "literal":
			item["use_perl_regexp"] = false
			delete(item, "syntax")
		}
	}
	delete(item, "contextAround")
	delete(item, "outputMode")
}

func safePatternAlias(pattern string, syntax any) bool {
	value, _ := syntax.(string)
	return !strings.EqualFold(strings.TrimSpace(value), "literal") || !strings.Contains(pattern, `\`)
}

func searchSharedArguments(args map[string]any) map[string]any {
	shared := make(map[string]any, 3)
	for _, key := range []string{"file_patterns", "case_sensitive", "use_perl_regexp", "syntax"} {
		if value, ok := args[key]; ok {
			shared[key] = value
		}
	}
	return shared
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
