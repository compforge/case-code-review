package recording

import (
	"encoding/json"

	"github.com/compforge/agentgo"
	"github.com/qiankunli/case-code-review/internal/harness/msg"
	"github.com/qiankunli/case-code-review/internal/llm"
)

func nativeRequest(request llm.ChatRequest) *agentgo.LLMRequest {
	out := &agentgo.LLMRequest{Config: &agentgo.CallConfig{ThinkingLevel: agentgo.NormalizeThinkingLevel(agentgo.ThinkingLevel(request.ReasoningEffort)), MaxTokens: request.MaxTokens}}
	for _, m := range msg.Wrap(request.Messages) {
		wire, _ := m.ToMessage()
		out.Messages = append(out.Messages, wire)
	}
	for _, t := range request.Tools {
		out.Tools = append(out.Tools, agentgo.ToolSpec{Name: t.Function.Name, Description: t.Function.Description, Parameters: t.Function.Parameters})
	}
	return out
}
func nativeResponse(response *llm.ChatResponse) agentgo.Message {
	choice := response.Choices[0]
	m := agentgo.Message{Role: agentgo.RoleAssistant, StopReason: agentgo.StopReason(choice.FinishReason), Metadata: map[string]any{"model": response.Model, "alias": response.Alias}}
	if choice.Message.Content != nil {
		m.Content = append(m.Content, agentgo.TextBlock(*choice.Message.Content))
	}
	if choice.Message.ReasoningContent != "" {
		m.Content = append(m.Content, agentgo.ThinkingBlock(choice.Message.ReasoningContent))
	}
	for _, c := range choice.Message.ToolCalls {
		args := json.RawMessage(c.Function.Arguments)
		call := agentgo.ToolCall{ID: c.ID, Name: c.Function.Name, Args: args}
		if !json.Valid(args) {
			call.Args = json.RawMessage(`{}`)
			call.ArgsInvalid = true
			call.ArgsRawText = c.Function.Arguments
		}
		m.Content = append(m.Content, agentgo.ToolCallBlock(call))
	}
	if u := response.Usage; u != nil {
		m.Usage = &agentgo.Usage{Provider: response.Alias, Model: response.Model, Input: int(u.PromptTokens - u.CacheWriteTokens), Output: int(u.CompletionTokens), Reasoning: int(u.ReasoningTokens), CacheRead: int(u.CacheReadTokens), CacheWrite: int(u.CacheWriteTokens), TotalTokens: int(u.TotalTokens)}
	}
	return m
}

func wireMessages(messages []agentgo.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		wire := llm.NewTextMessage(string(m.Role), m.TextContent())
		wire.ReasoningContent = m.ThinkingContent()
		wire.ToolCallID, _ = m.Metadata["tool_call_id"].(string)
		for _, c := range m.ToolCalls() {
			args := string(c.Args)
			if c.ArgsInvalid {
				args = c.ArgsRawText
			}
			wire.ToolCalls = append(wire.ToolCalls, llm.ToolCall{ID: c.ID, Type: "function", Function: llm.FunctionCall{Name: c.Name, Arguments: args}})
		}
		out = append(out, wire)
	}
	return out
}
