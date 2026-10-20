package llm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/compforge/agentgo"
	models "github.com/compforge/agentgo/llm"
	"github.com/voocel/litellm/retry"
)

// modelClient is the boundary between CCR's task DTOs and AgentGo's model
// contract. AgentGo owns protocol serialization, reasoning and routing.
//
// +spec=`Native Harness requests retain message metadata, raw provider history and all call options`
// +link=component://docs/harness.md
type modelClient struct {
	model    agentgo.ChatModel
	err      error
	cfg      ClientConfig
	protocol string
}

func NewOpenAIClient(cfg ClientConfig) *modelClient    { return newModelClient("compat", cfg) }
func NewAnthropicClient(cfg ClientConfig) *modelClient { return newModelClient("anthropic", cfg) }

func newModelClient(protocol string, cfg ClientConfig) *modelClient {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Minute
	}
	url := strings.TrimRight(cfg.URL, "/")
	if protocol == "anthropic" {
		if !strings.HasSuffix(url, "/v1/messages") {
			url += "/v1/messages"
		}
	} else if !strings.HasSuffix(url, "/chat/completions") {
		url += "/chat/completions"
	}
	cfg.URL = url
	client := &modelClient{cfg: cfg, protocol: protocol}
	if cfg.Model != "" {
		client.model, client.err = client.newModel(cfg.Model)
	}
	return client
}

func (c *modelClient) newModel(name string) (agentgo.ChatModel, error) {
	base := strings.TrimSuffix(c.cfg.URL, "/chat/completions")
	if c.protocol == "anthropic" {
		base = strings.TrimSuffix(c.cfg.URL, "/v1/messages")
	}
	// Scope opaque provider replay state to the configured endpoint.
	endpointID := sha256.Sum256([]byte(c.cfg.URL))
	options := []models.ModelOption{
		models.WithProviderExtra(map[string]any{"name": fmt.Sprintf("%s-%x", c.protocol, endpointID[:8])}),
		models.WithAPIKey(c.cfg.APIKey), models.WithBaseURL(base),
		models.WithRequestTimeout(c.cfg.Timeout), models.WithExtra(c.cfg.ExtraBody),
		models.WithHTTPTransport(&clientTransport{cfg: c.cfg, anthropic: c.protocol == "anthropic"}),
	}

	if c.cfg.MaxRetries > 0 {
		policy := retry.DefaultPolicy()
		policy.MaxAttempts = c.cfg.MaxRetries + 1
		options = append(options, models.WithResilience(models.ResilienceConfig{Retry: policy}))
	}
	model, err := models.NewModel(c.protocol, name, options...)
	if err != nil {
		return nil, err
	}
	model.GetConfig().MaxTokens = 0
	if c.protocol == "anthropic" {
		model.GetConfig().MaxTokens = 8192
	}
	return model, nil
}

func (c *modelClient) CompletionsWithCtx(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	model := c.model
	if model == nil {
		var err error
		model, err = c.newModel(req.Model)
		if err != nil {
			return nil, err
		}
	}
	request, err := modelRequest(req)
	if err != nil {
		return nil, err
	}
	// The system breakpoint also covers the preceding tool prefix on Anthropic.
	// Preserve explicitly supplied cache placement and never mutate loop history.
	request.Messages = append([]agentgo.Message(nil), request.Messages...)
	for i := len(request.Messages) - 1; i >= 0; i-- {
		if request.Messages[i].Role == agentgo.RoleSystem {
			metadata := maps.Clone(request.Messages[i].Metadata)
			if metadata == nil {
				metadata = make(map[string]any)
			}
			if _, exists := metadata["cache_control"]; !exists {
				metadata["cache_control"] = "ephemeral"
			}
			request.Messages[i].Metadata = metadata
			break
		}
	}
	progress := newRequestTrace()
	defer progress.finish()
	callCtx := context.WithValue(ctx, requestTraceKey{}, progress)
	options := []agentgo.CallOption{}
	if request.Config != nil {
		config := *request.Config
		options = append(options, func(c *agentgo.CallConfig) { *c = config })
	}
	response, err := model.Generate(callCtx, request.Messages, request.Tools, options...)
	if err != nil {
		var result *ChatResponse
		if response != nil {
			result = modelResponse(response.Message)
		}
		return result, annotateRequestError(ctx, err, progress)
	}
	if response == nil {
		return nil, fmt.Errorf("llm: model returned no response")
	}
	return modelResponse(response.Message), nil
}

func modelRequest(req ChatRequest) (agentgo.LLMRequest, error) {
	if req.Native != nil {
		return *req.Native, nil
	}
	config := &agentgo.CallConfig{MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	if req.ReasoningEffort != "" && req.ReasoningEffort != "auto" {
		config.ThinkingLevel = agentgo.NormalizeThinkingLevel(agentgo.ThinkingLevel(req.ReasoningEffort))
	}
	if req.ToolChoice != nil {
		config.ToolChoice = req.ToolChoice.Mode
		if req.ToolChoice.Name != "" {
			config.ToolChoice = map[string]any{"type": "function", "function": map[string]any{"name": req.ToolChoice.Name}}
		}
	}
	request := agentgo.LLMRequest{Config: config}
	for _, message := range req.Messages {
		m := agentgo.Message{Role: agentgo.Role(message.Role)}
		if message.ReasoningContent != "" {
			m.Content = append(m.Content, agentgo.ThinkingBlock(message.ReasoningContent))
		}
		if text := message.ExtractText(); text != "" {
			m.Content = append(m.Content, agentgo.TextBlock(text))
		}
		if message.Role == "tool" {
			m.Metadata = map[string]any{"tool_call_id": message.ToolCallID}
		}
		for _, call := range message.ToolCalls {
			if !json.Valid([]byte(call.Function.Arguments)) {
				return request, fmt.Errorf("invalid tool call arguments for %s", call.Function.Name)
			}
			m.Content = append(m.Content, agentgo.ToolCallBlock(agentgo.ToolCall{ID: call.ID, Name: call.Function.Name, Args: json.RawMessage(call.Function.Arguments)}))
		}
		// Preserve legacy multipart tool results as individual native messages.
		if blocks, ok := message.Content.([]ContentBlock); ok {
			m.Content = nil
			for _, block := range blocks {
				if block.Type == "tool_result" {
					request.Messages = append(request.Messages, agentgo.Message{Role: agentgo.RoleTool, Content: []agentgo.ContentBlock{agentgo.TextBlock(extractBlockText(block))}, Metadata: map[string]any{"tool_call_id": block.ToolUseID}})
				} else {
					m.Content = append(m.Content, agentgo.TextBlock(extractBlockText(block)))
				}
			}
			if len(m.Content) == 0 {
				continue
			}
		}
		request.Messages = append(request.Messages, m)
	}
	for _, tool := range req.Tools {
		request.Tools = append(request.Tools, agentgo.ToolSpec{Name: tool.Function.Name, Description: tool.Function.Description, Parameters: tool.Function.Parameters})
	}
	return request, nil
}

func modelResponse(message agentgo.Message) *ChatResponse {
	text := message.TextContent()
	calls := message.ToolCalls()
	toolCalls := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		args := string(call.Args)
		if call.ArgsInvalid {
			args = call.ArgsRawText
		}
		toolCalls = append(toolCalls, ToolCall{ID: call.ID, Type: "function", Function: FunctionCall{Name: call.Name, Arguments: args}})
	}
	response := &ChatResponse{Native: &message, Choices: []Choice{{Message: ResponseMessage{Role: "assistant", Content: &text, ReasoningContent: message.ThinkingContent(), ToolCalls: toolCalls}, FinishReason: string(message.StopReason)}}}
	if raw, ok := message.Metadata["finish_reason_raw"].(string); ok && raw != "" {
		response.Choices[0].FinishReason = raw
	}
	response.Alias, _ = message.Metadata["route_endpoint"].(string)
	response.ID, _ = message.Metadata["id"].(string)
	if usage := message.Usage; usage != nil {
		response.Model = usage.Model
		response.Usage = &UsageInfo{PromptTokens: int64(usage.Input + usage.CacheWrite), CompletionTokens: int64(usage.Output), ReasoningTokens: int64(usage.Reasoning), CacheReadTokens: int64(usage.CacheRead), CacheWriteTokens: int64(usage.CacheWrite), TotalTokens: int64(usage.TotalTokens)}
	}
	return response
}

type requestTraceKey struct{}
type clientTransport struct {
	cfg       ClientConfig
	anthropic bool
}

// Authentication policy and transport progress belong to CCR's connection
// configuration; request/response protocol handling remains in AgentGo.
func (t *clientTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.Header.Set("User-Agent", userAgent(""))
	if t.anthropic {
		header, _ := NormalizeAuthHeader(t.cfg.AuthHeader)
		if header == "" {
			header = "authorization"
		}
		value := request.Header.Get("X-Api-Key")
		if value == "" {
			value = strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		}
		request.Header.Del("X-Api-Key")
		request.Header.Del("Authorization")
		if header == "authorization" {
			value = "Bearer " + value
		}
		request.Header.Set(header, value)
	}
	next := http.DefaultTransport.RoundTrip
	if progress, ok := request.Context().Value(requestTraceKey{}).(*requestTrace); ok {
		return progress.middleware(request, next)
	}
	return next(request)
}
