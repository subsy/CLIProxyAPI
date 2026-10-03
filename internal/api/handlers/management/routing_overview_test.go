package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/dashboard"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestGetRoutingOverviewGroupsAndOrdersSubscriptions(t *testing.T) {
	now := time.Now()
	manager := coreauth.NewManager(nil, nil, nil)
	claudeAuth := func(id string, reset time.Time) *coreauth.Auth {
		return &coreauth.Auth{
			ID:         id,
			Provider:   "claude",
			Attributes: map[string]string{"runtime_only": "true", coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
			Metadata:   map[string]any{"email": id + "@example.com"},
			Quota: coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.4",
				"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(reset.Unix(), 10),
			}},
		}
	}
	for _, auth := range []*coreauth.Auth{
		claudeAuth("claude-late", now.Add(120*time.Hour)),
		claudeAuth("claude-soon", now.Add(30*time.Hour)),
		{ID: "codex-new", Provider: "codex", Attributes: map[string]string{"runtime_only": "true", coreauth.AttributeAuthKind: coreauth.AuthKindOAuth}, Metadata: map[string]any{"plan_type": "pro"}},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{Routing: config.RoutingConfig{Strategy: "reset-soonest"}}, manager)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/routing/overview", nil)
	h.GetRoutingOverview(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Routing  struct{ Strategy string } `json:"routing"`
		Warnings []routingOverviewWarning  `json:"warnings"`
		Groups   []struct {
			Provider    string
			Plan        string
			Credentials []routingOverviewCredential
		} `json:"groups"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode: %v", errDecode)
	}
	if body.Routing.Strategy != "reset-soonest" || len(body.Groups) != 2 {
		t.Fatalf("overview = %s", rec.Body.String())
	}
	claude := body.Groups[0]
	if claude.Provider != "claude" || len(claude.Credentials) != 2 {
		t.Fatalf("claude group = %+v", claude)
	}
	if claude.Credentials[0].ID != "claude-soon" || claude.Credentials[0].RoutingOrder != 1 || claude.Credentials[0].RoutingTier != "known" {
		t.Fatalf("first claude credential = %+v, want claude-soon routed first", claude.Credentials[0])
	}
	if len(claude.Credentials[0].Windows) != 1 || claude.Credentials[0].Windows[0].Label != "7d" {
		t.Fatalf("claude windows = %+v", claude.Credentials[0].Windows)
	}
	codex := body.Groups[1]
	if codex.Plan != "pro" || !codex.Credentials[0].Websockets || codex.Credentials[0].RoutingTier != "discover" {
		t.Fatalf("codex group = %+v", codex)
	}
	foundAffinityWarning := false
	for _, warning := range body.Warnings {
		if warning.Code == "session_affinity_disabled" {
			foundAffinityWarning = true
		}
	}
	if !foundAffinityWarning {
		t.Fatalf("warnings = %+v, want session_affinity_disabled", body.Warnings)
	}
}

func TestProjectQuotaWindowUse(t *testing.T) {
	now := time.Now()
	window := coreauth.QuotaWindow{WindowMinutes: 7 * 24 * 60, UsedFraction: 0.2, ResetAt: now.Add(84 * time.Hour)}
	projected := dashboard.ProjectWindowUse(window, now)
	if projected == nil || *projected < 0.39 || *projected > 0.41 {
		t.Fatalf("projected = %v, want ~0.4 (half the window elapsed at 20%%)", projected)
	}
	window.ResetAt = now.Add(7*24*time.Hour - time.Hour)
	if dashboard.ProjectWindowUse(window, now) != nil {
		t.Fatalf("projection for a window that just started should be nil")
	}
}
