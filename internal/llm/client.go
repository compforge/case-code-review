// Package llm configures native AgentGo models and applies CCR review budgets.
package llm

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/compforge/agentgo"
	tiktoken "github.com/pkoukk/tiktoken-go"
)

var AppVersion = "dev"

func userAgent(provider string) string {
	ua := "case-code-review/" + AppVersion
	if provider != "" {
		ua += " | " + provider
	}
	return ua
}

// ToolDef defines a tool/function available to the model.
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

// FunctionDef specifies the metadata for a tool definition.
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// ModelConfig holds configuration for connecting to an LLM service.
type ModelConfig struct {
	URL        string         // Full API endpoint URL
	APIKey     string         // Bearer token / API key
	Model      string         // Default model override
	AuthHeader string         // Auth header name: "x-api-key", "authorization", or empty for protocol default
	Timeout    time.Duration  // Request timeout
	ExtraBody  map[string]any // Vendor-specific fields merged into every request body
}

// --- Factory ---

// NewModel configures one AgentGo model from an application endpoint.
func NewModel(ep ResolvedEndpoint) agentgo.ChatModel {
	cfg := ModelConfig{
		URL:        ep.URL,
		APIKey:     ep.Token,
		Model:      ep.Model,
		AuthHeader: ep.AuthHeader,
		Timeout:    ep.Timeout,
		ExtraBody:  ep.ExtraBody,
	}
	if ep.Protocol == "anthropic" {
		return newAnthropicModel(cfg)
	}
	return newOpenAIModel(cfg)
}

// --- Token counting with tiktoken ---

// modelTokenizerCache caches initialized tiktoken encoders keyed by encoding name.
type modelTokenizerCache struct {
	mu    sync.RWMutex
	cache map[string]*tiktoken.Tiktoken
}

func newModelTokenizerCache() *modelTokenizerCache {
	return &modelTokenizerCache{cache: make(map[string]*tiktoken.Tiktoken)}
}

func (c *modelTokenizerCache) getOrLoad(encName string) (*tiktoken.Tiktoken, error) {
	c.mu.RLock()
	if tke, ok := c.cache[encName]; ok {
		c.mu.RUnlock()
		return tke, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if tke, ok := c.cache[encName]; ok {
		return tke, nil
	}
	enc, err := tiktoken.GetEncoding(encName)
	if err != nil {
		return nil, fmt.Errorf("get tiktoken encoding %q: %w", encName, err)
	}
	c.cache[encName] = enc
	return enc, nil
}

var defaultTokenizer = newModelTokenizerCache()

func countTokensWithEncoding(text string, encName string) int {
	tke, err := defaultTokenizer.getOrLoad(encName)
	if err != nil {
		return len([]byte(text)) / 4
	}
	return len(tke.Encode(text, nil, nil))
}

func CountTokens(text string) int {
	return CountTokensForModel(text, "")
}

func CountTokensForModel(text string, modelName string) int {
	if text == "" {
		return 0
	}
	encName := encodingForModel(modelName)
	return countTokensWithEncoding(text, encName)
}

func encodingForModel(modelName string) string {
	lower := strings.ToLower(modelName)
	switch {
	case strings.Contains(lower, "o1") || strings.Contains(lower, "o3") || strings.Contains(lower, "o4"):
		return "o200k_base"
	default:
		return "cl100k_base"
	}
}

// stripThinkTags removes reasoning wrapper tags from content.
func stripThinkTags(s string) string {
	// Construct tag strings from individual bytes.
	openBytes := []byte{0x3c, 't', 'h', 'i', 'n', 'k', 0x3e}
	closeBytes := []byte{0x3c, 0x2f, 't', 'h', 'i', 'n', 'k', 0x3e}
	s = strings.ReplaceAll(s, string(openBytes), "")
	s = strings.ReplaceAll(s, string(closeBytes), "")
	return s
}
