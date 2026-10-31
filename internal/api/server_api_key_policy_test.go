package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// Every route that picks a Codex credential directly must refuse a key that may not use
// codex. Routes are found by handler, so a newly added one is checked too.
func TestCodexDirectRoutesRefuseKeysWithoutCodex(t *testing.T) {
	cfg := &config.Config{}
	cfg.APIKeys = []string{"open-key", "claude-key"}
	cfg.APIKeyPolicies = []config.APIKeyPolicy{{APIKey: "claude-key", AllowedProviders: []string{"claude"}}}
	server := newTestServerWithConfig(t, cfg)

	checked := 0
	for _, route := range server.engine.Routes() {
		if !strings.Contains(route.Handler, "codex/live") && !strings.Contains(route.Handler, "codexAlphaSearch") {
			continue
		}
		path := strings.ReplaceAll(route.Path, ":call_id", "call-123")
		for key, forbidden := range map[string]bool{"claude-key": true, "open-key": false} {
			req := httptest.NewRequest(route.Method, path, strings.NewReader("{}"))
			req.Header.Set("Authorization", "Bearer "+key)
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)
			if got := rec.Code == http.StatusForbidden; got != forbidden {
				t.Errorf("%s %s with %s: status %d, want forbidden=%v", route.Method, route.Path, key, rec.Code, forbidden)
			}
		}
		checked++
	}
	if checked < 15 {
		t.Fatalf("found only %d Codex-direct routes; the handler match is probably stale", checked)
	}
}
