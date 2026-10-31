package handlers

import (
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/net/context"
)

// ClientAPIKeyContextKey is the gin context key holding the authenticated client API key.
const ClientAPIKeyContextKey = "userApiKey"

// ClientAPIKey returns the client API key the request authenticated with, or "".
func ClientAPIKey(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get(ClientAPIKeyContextKey)
	if !exists {
		return ""
	}
	key, _ := value.(string)
	return key
}

func clientAPIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return ClientAPIKey(ginCtx)
}

// ModelAllowedForClientKey reports whether the request's client key may use any provider that
// serves model. Unrestricted keys may use every model.
func ModelAllowedForClientKey(cfg *config.SDKConfig, c *gin.Context, model string) bool {
	allowed, restricted := cfg.AllowedProvidersForAPIKey(ClientAPIKey(c))
	if !restricted {
		return true
	}
	for _, provider := range util.GetProviderName(model) {
		if _, ok := allowed[internalconfig.NormalizeProviderID(provider)]; ok {
			return true
		}
	}
	return false
}

// restrictProvidersForClientKey drops providers the request's client key may not use. It
// returns 403 when none remain, so a restricted key never reaches another provider's accounts.
func (h *BaseAPIHandler) restrictProvidersForClientKey(ctx context.Context, providers []string, model string) ([]string, *interfaces.ErrorMessage) {
	if h == nil {
		return providers, nil
	}
	allowed, restricted := h.Cfg.AllowedProvidersForAPIKey(clientAPIKeyFromContext(ctx))
	if !restricted {
		return providers, nil
	}
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		if _, ok := allowed[internalconfig.NormalizeProviderID(provider)]; ok {
			out = append(out, provider)
		}
	}
	if len(out) == 0 {
		return nil, clientKeyForbidden(fmt.Sprintf("model %q is not available to this API key", model))
	}
	return out, nil
}

// denyPluginExecutorForRestrictedKey refuses plugin-executor routes for restricted keys: a
// plugin executor is not tied to a provider, so the key's limits could not be enforced.
func (h *BaseAPIHandler) denyPluginExecutorForRestrictedKey(ctx context.Context, model string) *interfaces.ErrorMessage {
	if h == nil {
		return nil
	}
	if _, restricted := h.Cfg.AllowedProvidersForAPIKey(clientAPIKeyFromContext(ctx)); !restricted {
		return nil
	}
	return clientKeyForbidden(fmt.Sprintf("model %q is routed to a plugin executor, which this API key may not use", model))
}

func clientKeyForbidden(message string) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: fmt.Errorf("%s", message)}
}
