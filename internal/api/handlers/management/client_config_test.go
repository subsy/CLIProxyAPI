package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestGetClientConfig(t *testing.T) {
	cfg := &config.Config{Port: 8317}
	cfg.APIKeys = []string{"key-1"}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)
	serve := func(client, query, requestHost string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/client-config/"+client+query, nil)
		ctx.Request.Host = requestHost
		ctx.Params = gin.Params{{Key: "client", Value: client}}
		h.GetClientConfig(ctx)
		return rec
	}

	cases := []struct {
		client, query, requestHost, want string
	}{
		{"codex", "", "desktop.tailnet.ts.net:8317", `base_url = "http://desktop.tailnet.ts.net:8317/v1"`},
		{"codex", "", "[fd7a::1]:8317", `base_url = "http://[fd7a::1]:8317/v1"`},
		{"claude", "?host=box", "desktop:8317", `ANTHROPIC_BASE_URL="http://box:8317"`},
	}
	for _, tc := range cases {
		rec := serve(tc.client, tc.query, tc.requestHost)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s%s via %s: %d\n%s", tc.client, tc.query, tc.requestHost, rec.Code, rec.Body.String())
		}
	}
	if rec := serve("cursor", "", "desktop:8317"); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown client status = %d", rec.Code)
	}
}
