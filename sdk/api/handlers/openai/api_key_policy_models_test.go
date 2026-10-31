package openai

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestOpenAIModelsHidesModelsTheKeyCannotUse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	modelRegistry := registry.GetGlobalRegistry()
	now := time.Now().Unix()
	modelRegistry.RegisterClient("test-policy-list-claude", "claude", []*registry.ModelInfo{{ID: "policy-list-claude", Object: "model", Created: now}})
	modelRegistry.RegisterClient("test-policy-list-codex", "codex", []*registry.ModelInfo{{ID: "policy-list-codex", Object: "model", Created: now}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("test-policy-list-claude")
		modelRegistry.UnregisterClient("test-policy-list-codex")
	})
	h := NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		APIKeys:        []string{"open-key", "codex-key"},
		APIKeyPolicies: []sdkconfig.APIKeyPolicy{{APIKey: "codex-key", AllowedProviders: []string{"codex"}}},
	}, coreauth.NewManager(nil, nil, nil)))

	ids := func(key string) map[string]bool {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		c.Set(handlers.ClientAPIKeyContextKey, key)
		h.OpenAIModels(c)
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v: %s", key, err, rec.Body.String())
		}
		out := map[string]bool{}
		for _, m := range body.Data {
			out[m.ID] = true
		}
		return out
	}
	if got := ids("open-key"); !got["policy-list-claude"] || !got["policy-list-codex"] {
		t.Errorf("unrestricted key should see both models, got %v", got)
	}
	if got := ids("codex-key"); got["policy-list-claude"] || !got["policy-list-codex"] {
		t.Errorf("codex-only key should see only the codex model, got %v", got)
	}
}
