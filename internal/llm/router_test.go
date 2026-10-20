package llm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveModels_Chain(t *testing.T) {
	for _, k := range []string{"CCR_LLM_URL", "CCR_LLM_TOKEN", "CCR_LLM_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"} {
		t.Setenv(k, "")
	}
	cfg := configFile{
		Routing: routingConfig{
			Models: []modelRef{{Provider: "p1", Alias: "primary"}, {Provider: "p2", Model: "m2b"}},
			Policy: "priority",
		},
		CustomProviders: map[string]providerEntryConfig{
			"p1": {URL: "https://a.example.com", Protocol: "openai", APIKey: "k1", Model: "m1"},
			"p2": {URL: "https://b.example.com", Protocol: "openai", APIKey: "k2", Model: "m2"},
		},
	}
	data, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	eps, routing, err := ResolveModels(cfgPath)
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if routing.Policy != policyPriority {
		t.Errorf("policy=%q, want %q", routing.Policy, policyPriority)
	}
	if routing.CallTimeout != defaultRoutingCallTimeout {
		t.Errorf("routing call timeout=%v, want %v", routing.CallTimeout, defaultRoutingCallTimeout)
	}
	if len(eps) != 2 {
		t.Fatalf("pool size=%d, want 2", len(eps))
	}
	if eps[0].Model != "m1" {
		t.Errorf("eps[0].Model=%q, want m1", eps[0].Model)
	}
	if eps[1].Model != "m2b" { // explicit ref.Model wins over the provider's default m2
		t.Errorf("eps[1].Model=%q, want m2b (ref.Model overrides provider default)", eps[1].Model)
	}
	if eps[0].Alias != "primary" { // routing.models[].alias flows to the endpoint
		t.Errorf("eps[0].Alias=%q, want primary", eps[0].Alias)
	}
}

func TestResolveModels_RejectsUnknownPolicy(t *testing.T) {
	for _, k := range []string{"CCR_LLM_URL", "CCR_LLM_TOKEN", "CCR_LLM_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"} {
		t.Setenv(k, "")
	}
	cfg := configFile{
		Routing: routingConfig{
			Models: []modelRef{{Provider: "p1"}},
			Policy: "weighted", // not supported yet → reserved, must error rather than silently ignore
		},
		CustomProviders: map[string]providerEntryConfig{
			"p1": {URL: "https://a.example.com", Protocol: "openai", APIKey: "k1", Model: "m1"},
		},
	}
	data, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveModels(cfgPath); err == nil {
		t.Fatal("expected error for unsupported routing.policy, got nil")
	}
}

func TestResolveModels_AcceptsRoundRobin(t *testing.T) {
	for _, k := range []string{"CCR_LLM_URL", "CCR_LLM_TOKEN", "CCR_LLM_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL"} {
		t.Setenv(k, "")
	}
	cfg := configFile{
		Routing: routingConfig{
			Models: []modelRef{{Provider: "p1"}, {Provider: "p2"}},
			Policy: "round-robin",
		},
		CustomProviders: map[string]providerEntryConfig{
			"p1": {URL: "https://a.example.com", Protocol: "openai", APIKey: "k1", Model: "m1"},
			"p2": {URL: "https://b.example.com", Protocol: "openai", APIKey: "k2", Model: "m2"},
		},
	}
	data, _ := json.Marshal(cfg)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, routing, err := ResolveModels(cfgPath)
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if routing.Policy != policyRoundRobin {
		t.Errorf("policy=%q, want %q", routing.Policy, policyRoundRobin)
	}
}
