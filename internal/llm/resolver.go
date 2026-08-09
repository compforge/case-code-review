package llm

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ResolvedEndpoint holds the resolved LLM endpoint configuration.
type ResolvedEndpoint struct {
	URL        string
	Token      string
	Model      string
	Protocol   string         // "anthropic" or "openai"
	AuthHeader string         // Anthropic auth header: "x-api-key" or "authorization"
	Source     string         // human-readable config source label
	ExtraBody  map[string]any // vendor-specific request body fields
	MaxRetries int            // internal SDK retry budget (0 = SDK default, negative = disabled); not read from config — routed pools disable it so Router owns recovery
	Alias      string         // routing alias (routing.models[].alias); stamped onto comments this endpoint produces
	Timeout    time.Duration  // per-request HTTP timeout; 0 means client default (5 min). A stalled call fails here instead of hanging the whole review.
}

// Environment variable names for OCR-specific configuration.
const (
	envCCRLLMURL        = "CCR_LLM_URL"
	envCCRLLMToken      = "CCR_LLM_TOKEN"
	envCCRLLMModel      = "CCR_LLM_MODEL"
	envCCRLLMAuthHeader = "CCR_LLM_AUTH_HEADER"
	envCCRLLMTimeout    = "CCR_LLM_TIMEOUT" // per-request HTTP timeout in seconds; global override over any config timeout_sec
	envCCRUseAnthropic  = "CCR_USE_ANTHROPIC"
)

// validateTimeoutSec converts a config/env timeout in seconds to a Duration,
// rejecting negative values and guarding against time.Duration overflow. 0 means
// "unset" (the client falls back to its default).
func validateTimeoutSec(sec int) (time.Duration, error) {
	if sec == 0 {
		return 0, nil
	}
	if sec < 0 {
		return 0, fmt.Errorf("timeout_sec must be non-negative, got %d", sec)
	}
	if int64(sec) > int64(math.MaxInt64/int64(time.Second)) {
		return 0, fmt.Errorf("timeout_sec %d overflows time.Duration", sec)
	}
	return time.Duration(sec) * time.Second, nil
}

// parseTimeoutEnv reads CCR_LLM_TIMEOUT (seconds). ok=false (nil err) when unset;
// a malformed/invalid value is a hard error rather than a silent fallback.
func parseTimeoutEnv() (time.Duration, bool, error) {
	raw := strings.TrimSpace(os.Getenv(envCCRLLMTimeout))
	if raw == "" {
		return 0, false, nil
	}
	sec, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false, fmt.Errorf("%s must be an integer (seconds): %w", envCCRLLMTimeout, err)
	}
	d, err := validateTimeoutSec(sec)
	if err != nil {
		return 0, false, fmt.Errorf("%s: %w", envCCRLLMTimeout, err)
	}
	return d, true, nil
}

// Environment variable names from Claude Code configuration.
const (
	envCCBaseURL = "ANTHROPIC_BASE_URL"
	envCCToken   = "ANTHROPIC_AUTH_TOKEN"
	envCCModel   = "ANTHROPIC_MODEL"
)

// ResolveEndpoint reads from 4 strategy sources in priority order.
// Each strategy requires all three fields (URL, Token, Model) to be non-empty.
// Returns the first valid strategy's result.
func ResolveEndpoint(configPath string) (ResolvedEndpoint, error) {
	return ResolveEndpointWithModelOverride(configPath, "")
}

// ResolveEndpointWithModelOverride resolves an endpoint like ResolveEndpoint,
// but uses modelOverride as the request model when it is non-empty. The override
// can also supply the otherwise required model for a configured endpoint.
func ResolveEndpointWithModelOverride(configPath, modelOverride string) (ResolvedEndpoint, error) {
	modelOverride = strings.TrimSpace(modelOverride)

	strategies := []struct {
		name string
		fn   func() (ResolvedEndpoint, bool, error)
	}{
		{"OCR config file", func() (ResolvedEndpoint, bool, error) { return tryOCRConfig(configPath, modelOverride) }},
		{"OCR environment", func() (ResolvedEndpoint, bool, error) { return tryOCREnv(modelOverride) }},
		{"Claude Code environment", func() (ResolvedEndpoint, bool, error) { return tryCCEnv(modelOverride) }},
		{"Shell rc file", func() (ResolvedEndpoint, bool, error) { return tryShellRC(modelOverride) }},
	}

	for _, s := range strategies {
		ep, ok, err := s.fn()
		if err != nil {
			return ResolvedEndpoint{}, fmt.Errorf("resolve %s: %w", s.name, err)
		}
		if ok && ep.URL != "" && ep.Token != "" && ep.Model != "" {
			if ep.Source == "" {
				ep.Source = s.name
			}
			ep.Model = stripModelSuffix(ep.Model)
			envTimeout, hasEnv, err := parseTimeoutEnv()
			if err != nil {
				return ResolvedEndpoint{}, err
			}
			if hasEnv { // env is a global override, wins over any config timeout_sec
				ep.Timeout = envTimeout
			}
			return ep, nil
		}
	}

	return ResolvedEndpoint{}, fmt.Errorf("no valid LLM endpoint configured; one of CCR_LLM_URL/CCR_LLM_TOKEN/CCR_LLM_MODEL, ~/.casecodereview/config.json, or ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN/ANTHROPIC_MODEL must be set")
}

// tryOCREnv reads OCR-specific environment variables.
func tryOCREnv(modelOverride string) (ResolvedEndpoint, bool, error) {
	url := os.Getenv(envCCRLLMURL)
	token := os.Getenv(envCCRLLMToken)
	model := os.Getenv(envCCRLLMModel)
	if modelOverride != "" {
		model = modelOverride
	}
	if url == "" || token == "" || model == "" {
		return ResolvedEndpoint{}, false, nil
	}

	useAnthropic := true // default true
	if v := os.Getenv(envCCRUseAnthropic); v != "" {
		lower := strings.ToLower(v)
		useAnthropic = lower == "true" || lower == "1" || lower == "yes"
	}

	protocol := "anthropic"
	if !useAnthropic {
		protocol = "openai"
	}

	var authHeader string
	if protocol == "anthropic" {
		var err error
		authHeader, err = NormalizeAuthHeader(os.Getenv(envCCRLLMAuthHeader))
		if err != nil {
			return ResolvedEndpoint{}, false, fmt.Errorf("OCR environment: %w", err)
		}
		if authHeader == "" {
			authHeader = defaultAuthHeader(protocol)
		}
	}

	return ResolvedEndpoint{URL: url, Token: token, Model: model, Protocol: protocol, AuthHeader: authHeader, Source: "OCR environment"}, true, nil
}

// llmFileConfig represents the llm section in config.json.
type llmFileConfig struct {
	URL          string         `json:"url,omitempty"`
	AuthToken    string         `json:"auth_token,omitempty"`
	AuthHeader   string         `json:"auth_header,omitempty"`
	Model        string         `json:"model,omitempty"`
	UseAnthropic *bool          `json:"use_anthropic,omitempty"` // pointer to distinguish unset from false
	TimeoutSec   int            `json:"timeout_sec,omitempty"`   // per-request HTTP timeout in seconds (0 = client default)
	ExtraBody    map[string]any `json:"extra_body,omitempty"`
}

// providerEntryConfig represents a single provider entry in config.json.
type providerEntryConfig struct {
	APIKey     string         `json:"api_key,omitempty"`
	URL        string         `json:"url,omitempty"`
	Protocol   string         `json:"protocol,omitempty"`
	Model      string         `json:"model,omitempty"`
	Models     []string       `json:"models,omitempty"`
	AuthHeader string         `json:"auth_header,omitempty"`
	TimeoutSec int            `json:"timeout_sec,omitempty"` // per-request HTTP timeout in seconds (0 = client default)
	ExtraBody  map[string]any `json:"extra_body,omitempty"`
}

// modelRef is one entry of the ordered routing pool: which configured provider to
// use and (optionally) which of its models. An empty Model falls back to the
// provider's default model.
type modelRef struct {
	Provider string `json:"provider"`
	Model    string `json:"model,omitempty"`
	Alias    string `json:"alias,omitempty"` // friendly label stamped onto comments this model produces
}

// routingConfig is the multi-model namespace: an ordered pool plus a selection
// policy. Grouping these under `routing` (vs a bare top-level list) gives the policy
// and future per-pool knobs a stable home, and avoids colliding with
// providers.<name>.models (which is a provider's model catalog, not a routing pool).
type routingConfig struct {
	Models         []modelRef `json:"models,omitempty"`           // ordered pool; index 0 is primary
	Policy         string     `json:"policy,omitempty"`           // selection policy: priority or round-robin
	CallTimeoutSec int        `json:"call_timeout_sec,omitempty"` // one logical completion across all fallovers; 0 = 180s default
}

// RoutingOptions is the resolved runtime policy for one model pool. CallTimeout is
// shared by every fallover attempt in a logical completion; provider request
// timeouts remain independent per endpoint.
type RoutingOptions struct {
	Policy      string
	CallTimeout time.Duration
}

type configFile struct {
	Provider        string                         `json:"provider,omitempty"`
	Model           string                         `json:"model,omitempty"`
	Routing         routingConfig                  `json:"routing,omitempty"`
	Providers       map[string]providerEntryConfig `json:"providers,omitempty"`
	CustomProviders map[string]providerEntryConfig `json:"custom_providers,omitempty"`
	Llm             llmFileConfig                  `json:"llm,omitempty"`
}

// loadConfigFile reads + parses the OCR config file. ok=false (nil err) when absent.
func loadConfigFile(path string) (configFile, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return configFile{}, false, nil
		}
		return configFile{}, false, err
	}
	var cfg configFile
	if err := json.Unmarshal(data, &cfg); err != nil {
		return configFile{}, false, fmt.Errorf("parse config: %w", err)
	}
	return cfg, true, nil
}

// tryOCRConfig resolves a single endpoint from the config file (the primary, for
// callers that want one client). `provider` wins when set; otherwise `models[0]`
// (the pool's primary); otherwise the legacy `llm` block.
func tryOCRConfig(path, modelOverride string) (ResolvedEndpoint, bool, error) {
	cfg, ok, err := loadConfigFile(path)
	if err != nil || !ok {
		return ResolvedEndpoint{}, false, err
	}

	if cfg.Provider != "" {
		return tryProviderConfig(cfg, modelOverride)
	}
	if len(cfg.Routing.Models) > 0 {
		ep, err := resolveModelRef(cfg, cfg.Routing.Models[0])
		if err != nil {
			return ResolvedEndpoint{}, false, err
		}
		return ep, true, nil
	}

	return tryLegacyLlmConfig(cfg, modelOverride)
}

// resolveModelRef resolves one pool entry, reusing tryProviderConfig by pinning it to
// this entry's provider. ref.Model is passed as the model override so it wins over the
// provider's default and is validated against the provider's available models.
func resolveModelRef(cfg configFile, ref modelRef) (ResolvedEndpoint, error) {
	if ref.Provider == "" {
		return ResolvedEndpoint{}, fmt.Errorf("models[] entry missing required 'provider' field")
	}
	sub := cfg
	sub.Provider = ref.Provider
	sub.Model = "" // don't let a top-level `model` leak into routing entries; model comes from ref.Model or the provider default
	sub.Routing = routingConfig{}
	ep, ok, err := tryProviderConfig(sub, ref.Model)
	if err != nil {
		return ResolvedEndpoint{}, err
	}
	if !ok || ep.URL == "" || ep.Token == "" || ep.Model == "" {
		return ResolvedEndpoint{}, fmt.Errorf("models[] entry {provider:%q model:%q} did not resolve to a complete endpoint", ref.Provider, ref.Model)
	}
	ep.Model = stripModelSuffix(ep.Model)
	ep.Alias = ref.Alias
	return ep, nil
}

// ResolveModels resolves the full ordered model pool plus its routing options.
func ResolveModels(configPath string) ([]ResolvedEndpoint, RoutingOptions, error) {
	return ResolveModelsWithModelOverride(configPath, "")
}

// ResolveModelsWithModelOverride returns the ordered pool of endpoints and its routing
// options. An explicit modelOverride (--model) bypasses the pool and
// pins a single endpoint (policy "priority"). Without it, a config `routing.models` list
// resolves to the whole chain; otherwise it falls back to single-endpoint resolution
// (env / single provider / legacy / shell), wrapped as a one-element pool — so existing
// configs behave exactly as before.
func ResolveModelsWithModelOverride(configPath, modelOverride string) ([]ResolvedEndpoint, RoutingOptions, error) {
	if strings.TrimSpace(modelOverride) == "" {
		if cfg, ok, err := loadConfigFile(configPath); err != nil {
			return nil, RoutingOptions{}, err
		} else if ok && len(cfg.Routing.Models) > 0 {
			policy := strings.TrimSpace(cfg.Routing.Policy)
			if policy == "" {
				policy = policyPriority
			}
			if policy != policyPriority && policy != policyRoundRobin {
				return nil, RoutingOptions{}, fmt.Errorf("unsupported routing.policy %q (want %q or %q)", policy, policyPriority, policyRoundRobin)
			}
			routingTimeout, err := validateTimeoutSec(cfg.Routing.CallTimeoutSec)
			if err != nil {
				return nil, RoutingOptions{}, fmt.Errorf("routing: %w", err)
			}
			if routingTimeout == 0 {
				routingTimeout = defaultRoutingCallTimeout
			}
			envTimeout, hasEnv, err := parseTimeoutEnv()
			if err != nil {
				return nil, RoutingOptions{}, err
			}
			eps := make([]ResolvedEndpoint, 0, len(cfg.Routing.Models))
			for _, ref := range cfg.Routing.Models {
				ep, err := resolveModelRef(cfg, ref)
				if err != nil {
					return nil, RoutingOptions{}, err
				}
				if hasEnv { // env is a global override across the whole routing pool
					ep.Timeout = envTimeout
				}
				eps = append(eps, ep)
			}
			return eps, RoutingOptions{Policy: policy, CallTimeout: routingTimeout}, nil
		}
	}

	ep, err := ResolveEndpointWithModelOverride(configPath, modelOverride)
	if err != nil {
		return nil, RoutingOptions{}, err
	}
	return []ResolvedEndpoint{ep}, RoutingOptions{Policy: policyPriority}, nil
}

// tryProviderConfig resolves an endpoint from the provider-based configuration.
func tryProviderConfig(cfg configFile, modelOverride string) (ResolvedEndpoint, bool, error) {
	preset, isPreset := LookupProvider(cfg.Provider)

	var entry providerEntryConfig
	var ok bool
	if isPreset {
		entry, ok = cfg.Providers[cfg.Provider]
	} else {
		entry, ok = cfg.CustomProviders[cfg.Provider]
	}
	if !ok {
		section := "providers"
		if !isPreset {
			section = "custom_providers"
		}
		return ResolvedEndpoint{}, false, fmt.Errorf("provider %q is set but not configured in %s section", cfg.Provider, section)
	}

	apiKey := entry.APIKey
	if apiKey == "" {
		if isPreset && preset.EnvVar != "" {
			apiKey = os.Getenv(preset.EnvVar)
		}
	}
	if apiKey == "" {
		return ResolvedEndpoint{}, false, fmt.Errorf("provider %q has no api_key configured and no environment variable fallback found", cfg.Provider)
	}

	var url, protocol, authHeader, model string
	var extraBody map[string]any

	if isPreset {
		url = preset.BaseURL
		protocol = preset.Protocol
		authHeader = preset.AuthHeader
		if entry.URL != "" {
			url = entry.URL
		}
		if entry.Protocol != "" {
			protocol = strings.ToLower(entry.Protocol)
		}
	} else {
		// Custom provider: url and protocol are required; model can come from cfg.Model.
		if entry.URL == "" || entry.Protocol == "" {
			return ResolvedEndpoint{}, false, fmt.Errorf("custom provider %q requires url and protocol fields", cfg.Provider)
		}
		if !strings.EqualFold(entry.Protocol, "anthropic") && !strings.EqualFold(entry.Protocol, "openai") {
			return ResolvedEndpoint{}, false, fmt.Errorf("custom provider %q has invalid protocol %q: must be \"anthropic\" or \"openai\"", cfg.Provider, entry.Protocol)
		}
		url = entry.URL
		protocol = strings.ToLower(entry.Protocol)
	}

	if cfg.Model != "" {
		model = cfg.Model
	}
	if entry.Model != "" {
		model = entry.Model
	}

	// Build available model list for validation.
	var availableModels []string
	if isPreset {
		availableModels = append(availableModels, preset.Models...)
	}
	availableModels = append(availableModels, entry.Models...)

	// Apply model override with validation.
	if modelOverride != "" {
		if len(availableModels) > 0 {
			if !modelListContains(availableModels, modelOverride) {
				return ResolvedEndpoint{}, false, fmt.Errorf(
					"model %q is not available for provider %q; available models: %s",
					modelOverride,
					cfg.Provider,
					strings.Join(availableModels, ", "),
				)
			}
		}
		model = modelOverride
	}

	if model == "" {
		return ResolvedEndpoint{}, false, fmt.Errorf("provider %q has no model configured; run 'ccr config model' to select one or pass --model", cfg.Provider)
	}

	if protocol == "anthropic" {
		var err error
		ah := "authorization"
		if isPreset && authHeader != "" {
			ah = authHeader
		}
		if entry.AuthHeader != "" {
			ah = entry.AuthHeader
		}
		authHeader, err = NormalizeAuthHeader(ah)
		if err != nil {
			return ResolvedEndpoint{}, false, fmt.Errorf("provider %q: %w", cfg.Provider, err)
		}
		if authHeader == "" {
			authHeader = defaultAuthHeader(protocol)
		}
	} else {
		authHeader = ""
	}

	extraBody = entry.ExtraBody

	if protocol == "anthropic" {
		url = ensureMessagesSuffix(url)
	}

	timeout, err := validateTimeoutSec(entry.TimeoutSec)
	if err != nil {
		return ResolvedEndpoint{}, false, fmt.Errorf("provider %q: %w", cfg.Provider, err)
	}

	return ResolvedEndpoint{
		URL:        url,
		Token:      apiKey,
		Model:      model,
		Protocol:   protocol,
		AuthHeader: authHeader,
		Source:     "provider:" + cfg.Provider,
		ExtraBody:  extraBody,
		Timeout:    timeout,
	}, true, nil
}

// tryLegacyLlmConfig resolves an endpoint from the legacy llm config block.
func tryLegacyLlmConfig(cfg configFile, modelOverride string) (ResolvedEndpoint, bool, error) {
	model := cfg.Llm.Model
	if modelOverride != "" {
		model = modelOverride
	}
	if cfg.Llm.URL == "" || cfg.Llm.AuthToken == "" || model == "" {
		return ResolvedEndpoint{}, false, nil
	}

	useAnthropic := true // default true
	if cfg.Llm.UseAnthropic != nil {
		useAnthropic = *cfg.Llm.UseAnthropic
	}

	protocol := "anthropic"
	if !useAnthropic {
		protocol = "openai"
	}

	var authHeader string
	if protocol == "anthropic" {
		var err error
		authHeader, err = NormalizeAuthHeader(cfg.Llm.AuthHeader)
		if err != nil {
			return ResolvedEndpoint{}, false, fmt.Errorf("OCR config file: %w", err)
		}
		if authHeader == "" {
			authHeader = defaultAuthHeader(protocol)
		}
	}

	timeout, err := validateTimeoutSec(cfg.Llm.TimeoutSec)
	if err != nil {
		return ResolvedEndpoint{}, false, fmt.Errorf("OCR config file: %w", err)
	}

	return ResolvedEndpoint{URL: cfg.Llm.URL, Token: cfg.Llm.AuthToken, Model: model, Protocol: protocol, AuthHeader: authHeader, Source: "OCR config file", ExtraBody: cfg.Llm.ExtraBody, Timeout: timeout}, true, nil
}

// tryCCEnv reads Claude Code environment variables.
func tryCCEnv(modelOverride string) (ResolvedEndpoint, bool, error) {
	baseURL := os.Getenv(envCCBaseURL)
	token := os.Getenv(envCCToken)
	model := os.Getenv(envCCModel)
	if modelOverride != "" {
		model = modelOverride
	}
	if baseURL == "" || token == "" || model == "" {
		return ResolvedEndpoint{}, false, nil
	}

	url := ensureMessagesSuffix(baseURL)

	// Claude Code environment tokens are OAuth/Bearer-style credentials.
	return ResolvedEndpoint{URL: url, Token: token, Model: model, Protocol: "anthropic", AuthHeader: "authorization", Source: "Claude Code environment"}, true, nil
}

// tryShellRC parses ~/.zshrc and ~/.bashrc for ANTHROPIC_* exports.
func tryShellRC(modelOverride string) (ResolvedEndpoint, bool, error) {
	files := shellRCFiles()
	for _, f := range files {
		ep, ok, err := parseShellRC(f, modelOverride)
		if err != nil || ok {
			return ep, ok, err
		}
	}
	return ResolvedEndpoint{}, false, nil
}

func shellRCFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	candidates := []string{
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".bashrc"),
		filepath.Join(home, ".bash_profile"),
		filepath.Join(home, ".profile"),
	}
	var valid []string
	for _, f := range candidates {
		if _, err := os.Stat(f); err == nil {
			valid = append(valid, f)
		}
	}
	return valid
}

var exportRe = regexp.MustCompile(`^export\s+(ANTHROPIC_\w+)\s*=\s*(?:"([^"]*)"|'([^']*)'|(.+))\s*$`)

var modelSuffixRe = regexp.MustCompile(`\[\d+m\]$`)

func stripModelSuffix(model string) string {
	return modelSuffixRe.ReplaceAllString(model, "")
}

func parseShellRC(path, modelOverride string) (ResolvedEndpoint, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ResolvedEndpoint{}, false, nil
	}

	var baseURL, token, model string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		matches := exportRe.FindStringSubmatch(line)
		if matches == nil {
			continue
		}
		key := matches[1]
		value := matches[2]
		if value == "" {
			value = matches[3]
		}
		if value == "" {
			value = matches[4]
		}
		value = strings.TrimSpace(value)

		switch key {
		case "ANTHROPIC_BASE_URL":
			baseURL = value
		case "ANTHROPIC_AUTH_TOKEN":
			token = value
		case "ANTHROPIC_MODEL":
			model = value
		}
	}
	if modelOverride != "" {
		model = modelOverride
	}

	if baseURL == "" || token == "" || model == "" {
		return ResolvedEndpoint{}, false, nil
	}

	url := ensureMessagesSuffix(baseURL)

	// Claude Code shell rc tokens are OAuth/Bearer-style credentials.
	return ResolvedEndpoint{URL: url, Token: token, Model: model, Protocol: "anthropic", AuthHeader: "authorization", Source: "Shell rc file"}, true, nil
}

func defaultAuthHeader(protocol string) string {
	// auth_header is Anthropic-only; OpenAI-compatible clients keep API key auth.
	if protocol == "anthropic" {
		return "authorization"
	}
	return ""
}

// modelListContains checks if a model exists in the available models list.
func modelListContains(models []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, model := range models {
		if strings.TrimSpace(model) == target {
			return true
		}
	}
	return false
}

// NormalizeAuthHeader normalizes an auth header value to a canonical form.
// It returns an error for unrecognized values.
func NormalizeAuthHeader(header string) (string, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return "", nil
	}
	switch strings.ToLower(header) {
	case "x-api-key":
		return "x-api-key", nil
	case "authorization", "bearer":
		return "authorization", nil
	default:
		return "", fmt.Errorf("unsupported auth_header value %q; expected \"x-api-key\" or \"authorization\"", header)
	}
}

// ensureMessagesSuffix appends /v1/messages to base URLs that lack a versioned path.
func ensureMessagesSuffix(rawURL string) string {
	u := strings.TrimRight(rawURL, "/")
	if strings.Contains(u, "/v1/") {
		// Already has versioned path — don't modify.
		return rawURL
	}
	return u + "/v1/messages"
}
