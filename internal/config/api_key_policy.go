package config

import "strings"

// APIKeyPolicy limits which upstream providers one client API key may reach.
// A key in api-keys with no policy is unrestricted.
type APIKeyPolicy struct {
	// APIKey is the client key, exactly as listed in api-keys.
	APIKey string `yaml:"api-key" json:"api-key"`

	// AllowedProviders lists the provider identifiers the key may use, such as "codex",
	// "claude", "gemini" or an openai-compatibility name. An empty list allows nothing.
	AllowedProviders []string `yaml:"allowed-providers" json:"allowed-providers"`
}

// AllowedProvidersForAPIKey reports the providers a client key may use. restricted is false
// when no policy names the key, meaning every provider is allowed. Provider identifiers are
// lower-cased; several policies for the same key are combined.
func (cfg *SDKConfig) AllowedProvidersForAPIKey(apiKey string) (allowed map[string]struct{}, restricted bool) {
	if cfg == nil || len(cfg.APIKeyPolicies) == 0 {
		return nil, false
	}
	apiKey = strings.TrimSpace(apiKey)
	for _, policy := range cfg.APIKeyPolicies {
		if apiKey == "" || strings.TrimSpace(policy.APIKey) != apiKey {
			continue
		}
		if allowed == nil {
			allowed = make(map[string]struct{}, len(policy.AllowedProviders))
		}
		restricted = true
		for _, provider := range policy.AllowedProviders {
			if provider = NormalizeProviderID(provider); provider != "" {
				allowed[provider] = struct{}{}
			}
		}
	}
	return allowed, restricted
}

// APIKeyAllowsProvider reports whether a client key may use provider.
func (cfg *SDKConfig) APIKeyAllowsProvider(apiKey, provider string) bool {
	allowed, restricted := cfg.AllowedProvidersForAPIKey(apiKey)
	if !restricted {
		return true
	}
	_, ok := allowed[NormalizeProviderID(provider)]
	return ok
}

// NormalizeProviderID trims and lower-cases a provider identifier for comparison.
func NormalizeProviderID(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}
