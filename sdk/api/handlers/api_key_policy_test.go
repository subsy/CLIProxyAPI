package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func policyTestContext(t *testing.T, apiKey string) context.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if apiKey != "" {
		c.Set(ClientAPIKeyContextKey, apiKey)
	}
	return context.WithValue(context.Background(), "gin", c)
}

func registerPolicyTestModels(t *testing.T) {
	t.Helper()
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()
	modelRegistry.RegisterClient("test-policy-claude", "claude", []*registry.ModelInfo{
		{ID: "policy-claude-model", Created: now},
		{ID: "policy-shared-model", Created: now},
	})
	modelRegistry.RegisterClient("test-policy-codex", "codex", []*registry.ModelInfo{
		{ID: "policy-codex-model", Created: now},
		{ID: "policy-shared-model", Created: now},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-policy-claude")
		modelRegistry.UnregisterClient("test-policy-codex")
	})
}

func policyTestHandler() *BaseAPIHandler {
	return NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		APIKeys:        []string{"open-key", "codex-key"},
		APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "codex-key", AllowedProviders: []string{"codex"}}},
	}, coreauth.NewManager(nil, nil, nil))
}

func TestRestrictProvidersForClientKey(t *testing.T) {
	h := policyTestHandler()
	tests := []struct {
		name      string
		key       string
		providers []string
		want      []string
		forbidden bool
	}{
		{name: "unrestricted key keeps every provider", key: "open-key", providers: []string{"claude", "codex"}, want: []string{"claude", "codex"}},
		{name: "no client key keeps every provider", providers: []string{"claude"}, want: []string{"claude"}},
		{name: "restricted key drops other providers", key: "codex-key", providers: []string{"claude", "codex"}, want: []string{"codex"}},
		{name: "restricted key with no allowed provider is forbidden", key: "codex-key", providers: []string{"claude"}, forbidden: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errMsg := h.restrictProvidersForClientKey(policyTestContext(t, tt.key), tt.providers, "m")
			if tt.forbidden {
				if errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
					t.Fatalf("want 403, got %+v", errMsg)
				}
				return
			}
			if errMsg != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, %+v; want %v", got, errMsg, tt.want)
			}
		})
	}
}

// A restricted key must be refused on every execution path before any credential is chosen.
func TestExecutionPathsRefuseDisallowedProviders(t *testing.T) {
	registerPolicyTestModels(t)
	h := policyTestHandler()
	ctx := policyTestContext(t, "codex-key")
	body := []byte(`{"model":"policy-claude-model"}`)

	if _, _, errMsg := h.ExecuteWithAuthManager(ctx, "openai-response", "policy-claude-model", body, ""); errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("execute: want 403, got %+v", errMsg)
	}
	if _, _, errMsg := h.ExecuteCountWithAuthManager(ctx, "claude", "policy-claude-model", body, ""); errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("count: want 403, got %+v", errMsg)
	}
	data, _, errChan := h.ExecuteStreamWithAuthManager(ctx, "openai-response", "policy-claude-model", body, "")
	if data != nil {
		t.Fatal("stream: want no data channel")
	}
	if errMsg := <-errChan; errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("stream: want 403, got %+v", errMsg)
	}

	// Control: the same request from an unrestricted key gets past the policy (it then fails
	// for lack of credentials, which is not a 403).
	if _, _, errMsg := h.ExecuteWithAuthManager(policyTestContext(t, "open-key"), "openai-response", "policy-claude-model", body, ""); errMsg != nil && errMsg.StatusCode == http.StatusForbidden {
		t.Fatalf("unrestricted key must not be refused by the policy: %+v", errMsg)
	}
}

func TestDenyPluginExecutorForRestrictedKey(t *testing.T) {
	h := policyTestHandler()
	if errMsg := h.denyPluginExecutorForRestrictedKey(policyTestContext(t, "open-key"), "m"); errMsg != nil {
		t.Fatalf("unrestricted key: %+v", errMsg)
	}
	if errMsg := h.denyPluginExecutorForRestrictedKey(policyTestContext(t, "codex-key"), "m"); errMsg == nil || errMsg.StatusCode != http.StatusForbidden {
		t.Fatalf("restricted key: want 403, got %+v", errMsg)
	}
}

func TestModelAllowedForClientKey(t *testing.T) {
	registerPolicyTestModels(t)
	cfg := policyTestHandler().Cfg
	c := policyTestContext(t, "codex-key").Value("gin").(*gin.Context)
	for model, want := range map[string]bool{"policy-codex-model": true, "policy-shared-model": true, "policy-claude-model": false} {
		if got := ModelAllowedForClientKey(cfg, c, model); got != want {
			t.Errorf("%s: got %v, want %v", model, got, want)
		}
	}
}
