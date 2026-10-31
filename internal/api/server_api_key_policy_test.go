package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
)

func TestRequireClientKeyProvider(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.APIKeys = []string{"open-key", "claude-key", "codex-key"}
	cfg.APIKeyPolicies = []config.APIKeyPolicy{
		{APIKey: "claude-key", AllowedProviders: []string{"claude"}},
		{APIKey: "codex-key", AllowedProviders: []string{"codex"}},
	}
	s := &Server{cfg: cfg}
	for key, want := range map[string]int{"open-key": http.StatusOK, "codex-key": http.StatusOK, "claude-key": http.StatusForbidden} {
		engine := gin.New()
		engine.GET("/live", func(c *gin.Context) { c.Set(handlers.ClientAPIKeyContextKey, key) }, s.requireClientKeyProvider("codex"), func(c *gin.Context) { c.Status(http.StatusOK) })
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/live", nil))
		if rec.Code != want {
			t.Errorf("%s: status %d, want %d", key, rec.Code, want)
		}
	}
}
