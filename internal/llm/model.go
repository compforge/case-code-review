package llm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/compforge/agentgo"
	models "github.com/compforge/agentgo/llm"
)

// configuredModel applies CCR's connection and cache policy to native calls.
// AgentGo owns protocol serialization, reasoning and routing.
//
// +spec=`Native Harness requests retain message metadata, raw provider history and all call options`
// +link=component://docs/harness.md
type configuredModel struct {
	model    agentgo.ChatModel
	err      error
	cfg      ModelConfig
	protocol string
}

func newOpenAIModel(cfg ModelConfig) *configuredModel    { return newConfiguredModel("compat", cfg) }
func newAnthropicModel(cfg ModelConfig) *configuredModel { return newConfiguredModel("anthropic", cfg) }

func newConfiguredModel(protocol string, cfg ModelConfig) *configuredModel {
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
	client := &configuredModel{cfg: cfg, protocol: protocol}
	if cfg.Model != "" {
		client.model, client.err = client.newModel(cfg.Model)
	}
	return client
}

func (c *configuredModel) newModel(name string) (agentgo.ChatModel, error) {
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

func (c *configuredModel) Generate(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (*agentgo.LLMResponse, error) {
	if c.err != nil {
		return nil, c.err
	}
	if c.model == nil {
		return nil, fmt.Errorf("llm: endpoint requires a model")
	}
	// Cache policy changes a request-local copy, never the accepted history.
	messages = append([]agentgo.Message(nil), messages...)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == agentgo.RoleSystem {
			metadata := maps.Clone(messages[i].Metadata)
			if metadata == nil {
				metadata = make(map[string]any)
			}
			if _, exists := metadata["cache_control"]; !exists {
				metadata["cache_control"] = "ephemeral"
			}
			messages[i].Metadata = metadata
			break
		}
	}
	progress := newRequestTrace()
	defer progress.finish()
	ctxWithTrace := context.WithValue(ctx, requestTraceKey{}, progress)
	response, err := c.model.Generate(ctxWithTrace, messages, tools, opts...)
	if err != nil {
		return response, annotateRequestError(ctx, err, progress)
	}
	if response == nil {
		return nil, fmt.Errorf("llm: model returned no response")
	}
	return response, nil
}
func (c *configuredModel) SupportsTools() bool { return c.model != nil && c.model.SupportsTools() }
func (c *configuredModel) GenerateStream(ctx context.Context, messages []agentgo.Message, tools []agentgo.ToolSpec, opts ...agentgo.CallOption) (<-chan agentgo.StreamEvent, error) {
	return CompletionStream(ctx, func() (*agentgo.LLMResponse, error) { return c.Generate(ctx, messages, tools, opts...) })
}

type requestTraceKey struct{}
type clientTransport struct {
	cfg       ModelConfig
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
