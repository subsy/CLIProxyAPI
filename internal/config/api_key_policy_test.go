package config

import "testing"

func TestAllowedProvidersForAPIKey(t *testing.T) {
	cfg := &SDKConfig{
		APIKeys: []string{"open", "codex-only", "none", "split"},
		APIKeyPolicies: []APIKeyPolicy{
			{APIKey: "codex-only", AllowedProviders: []string{" Codex "}},
			{APIKey: "none"},
			{APIKey: "split", AllowedProviders: []string{"claude"}},
			{APIKey: "split", AllowedProviders: []string{"gemini"}},
		},
	}
	if _, restricted := cfg.AllowedProvidersForAPIKey("open"); restricted {
		t.Fatal("a key without a policy must be unrestricted")
	}
	if _, restricted := cfg.AllowedProvidersForAPIKey(""); restricted {
		t.Fatal("an empty key must not match a policy")
	}
	if !cfg.APIKeyAllowsProvider("codex-only", "codex") || cfg.APIKeyAllowsProvider("codex-only", "claude") {
		t.Fatal("codex-only must allow codex (case and spaces ignored) and nothing else")
	}
	if cfg.APIKeyAllowsProvider("none", "codex") {
		t.Fatal("a policy with no providers must allow nothing")
	}
	if !cfg.APIKeyAllowsProvider("split", "claude") || !cfg.APIKeyAllowsProvider("split", "gemini") {
		t.Fatal("policies for the same key must be combined")
	}
	var nilCfg *SDKConfig
	if !nilCfg.APIKeyAllowsProvider("any", "claude") {
		t.Fatal("a nil config must not restrict anything")
	}
}

func TestAPIKeyPoliciesParseInBothFormats(t *testing.T) {
	for name, data := range map[string]string{
		"legacy": "api-keys: [\"a\", \"b\"]\napi-key-policies:\n  - api-key: \"b\"\n    allowed-providers: [\"codex\"]\n",
		"v8":     "access:\n  api-keys: [\"a\", \"b\"]\n  api-key-policies:\n    - api-key: \"b\"\n      allowed-providers: [\"codex\"]\n",
	} {
		cfg, err := ParseConfigBytes([]byte(data))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !cfg.APIKeyAllowsProvider("b", "codex") || cfg.APIKeyAllowsProvider("b", "claude") || !cfg.APIKeyAllowsProvider("a", "claude") {
			t.Errorf("%s: policies not loaded: %+v", name, cfg.APIKeyPolicies)
		}
	}
}

func TestOpenAICompatibilityNamesMatchRegistryKeys(t *testing.T) {
	cfg := &SDKConfig{APIKeyPolicies: []APIKeyPolicy{
		{APIKey: "by-name", AllowedProviders: []string{"Kimi"}},
		{APIKey: "by-key", AllowedProviders: []string{"openai-compatible-kimi"}},
	}}
	for _, key := range []string{"by-name", "by-key"} {
		if !cfg.APIKeyAllowsProvider(key, "openai-compatible-kimi") {
			t.Errorf("%s: should allow the registry key openai-compatible-kimi", key)
		}
	}
	if cfg.APIKeyAllowsProvider("by-key", "openai-compatible-openai-compatible-kimi") {
		t.Error("an already-prefixed name must not be prefixed again")
	}
}
