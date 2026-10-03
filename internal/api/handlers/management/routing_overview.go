package management

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/dashboard"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type routingOverviewWindow struct {
	coreauth.QuotaWindow
	// ProjectedUsedAtReset extrapolates the current burn rate to the reset time; nil when unknown.
	ProjectedUsedAtReset *float64 `json:"projected_used_at_reset,omitempty"`
}

type routingOverviewCredential struct {
	ID            string                    `json:"id"`
	AuthIndex     string                    `json:"auth_index,omitempty"`
	Name          string                    `json:"name"`
	Email         string                    `json:"email,omitempty"`
	Provider      string                    `json:"provider"`
	Plan          string                    `json:"plan,omitempty"`
	AuthKind      string                    `json:"auth_kind,omitempty"`
	Priority      int                       `json:"priority"`
	Status        string                    `json:"status"`
	StatusMessage string                    `json:"status_message,omitempty"`
	Disabled      bool                      `json:"disabled"`
	Unavailable   bool                      `json:"unavailable"`
	RetryAt       time.Time                 `json:"retry_at,omitempty"`
	Websockets    bool                      `json:"websockets"`
	NeedsRelogin  string                    `json:"needs_relogin,omitempty"`
	RoutingOrder  int                       `json:"routing_order"`
	RoutingTier   string                    `json:"routing_tier"`
	WeeklyResetAt time.Time                 `json:"weekly_reset_at,omitempty"`
	ObservedAt    time.Time                 `json:"quota_observed_at,omitempty"`
	Windows       []routingOverviewWindow   `json:"windows"`
	Usage         dashboard.CredentialUsage `json:"usage"`
}

type routingOverviewGroup struct {
	Provider    string                       `json:"provider"`
	Plan        string                       `json:"plan,omitempty"`
	Credentials []*routingOverviewCredential `json:"credentials"`
}

type routingOverviewWarning struct {
	Code    string `json:"code"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

var routingTierNames = map[int]string{
	coreauth.ResetTierDiscover:  "discover",
	coreauth.ResetTierKnown:     "known",
	coreauth.ResetTierUnknown:   "no-data",
	coreauth.ResetTierExhausted: "exhausted",
}

// GetRoutingOverview returns subscription quota windows, routing order, usage and
// configuration warnings for every credential, grouped by provider and plan.
func (h *Handler) GetRoutingOverview(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(503, gin.H{"error": "auth manager unavailable"})
		return
	}
	now := time.Now().UTC()
	strategy, affinity, affinityTTL := coreauth.RoutingStrategyRoundRobin, false, ""
	if h.cfg != nil {
		if normalized, ok := normalizeRoutingStrategy(h.cfg.Routing.Strategy); ok {
			strategy = normalized
		}
		affinity = h.cfg.Routing.SessionAffinity
		affinityTTL = h.cfg.Routing.SessionAffinityTTL
	}
	usageByAuth, affinityStats := dashboard.DefaultStats().Snapshot()

	byProvider := make(map[string][]*coreauth.Auth)
	entries := make(map[string]*routingOverviewCredential)
	for _, auth := range h.authManager.List() {
		if auth == nil || (isRuntimeOnlyAuth(auth) && auth.Disabled) {
			continue
		}
		auth.EnsureIndex()
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		byProvider[provider] = append(byProvider[provider], auth)
		entries[auth.ID] = buildRoutingOverviewCredential(auth, usageByAuth[auth.ID], now)
	}

	groupsByKey := make(map[string]*routingOverviewGroup)
	for provider, auths := range byProvider {
		orderRoutingCandidates(auths, entries, strategy, now)
		for index, auth := range auths {
			entry := entries[auth.ID]
			entry.RoutingOrder = index + 1
			key := provider + "\x00" + entry.Plan
			group := groupsByKey[key]
			if group == nil {
				group = &routingOverviewGroup{Provider: provider, Plan: entry.Plan}
				groupsByKey[key] = group
			}
			group.Credentials = append(group.Credentials, entry)
		}
	}
	groups := make([]*routingOverviewGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Provider != groups[j].Provider {
			return groups[i].Provider < groups[j].Provider
		}
		return groups[i].Plan < groups[j].Plan
	})

	c.JSON(200, gin.H{
		"observed_at": now,
		"routing": gin.H{
			"strategy":             strategy,
			"session_affinity":     affinity,
			"session_affinity_ttl": affinityTTL,
			"home_mode":            h.authManager.HomeEnabled(),
		},
		"affinity": affinityStats,
		"warnings": routingOverviewWarnings(strategy, affinity, byProvider, affinityStats),
		"alerts":   dashboard.DefaultAlerter().Active(),
		"groups":   groups,
	})
}

func buildRoutingOverviewCredential(auth *coreauth.Auth, usage dashboard.CredentialUsage, now time.Time) *routingOverviewCredential {
	unavailable, status, statusMessage, retryAt := reconcileAuthFileCooldownState(auth, now)
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	tier, weeklyReset := coreauth.ResetSoonestRank(auth, now)
	entry := &routingOverviewCredential{
		ID:            auth.ID,
		AuthIndex:     auth.Index,
		Name:          name,
		Email:         authEmail(auth),
		Provider:      strings.ToLower(strings.TrimSpace(auth.Provider)),
		Plan:          routingOverviewPlan(auth),
		AuthKind:      auth.AuthKind(),
		Priority:      coreauth.AuthPriority(auth),
		Status:        string(status),
		StatusMessage: statusMessage,
		Disabled:      auth.Disabled,
		Unavailable:   unavailable,
		RetryAt:       retryAt,
		Websockets:    coreauth.WebsocketsEnabled(auth),
		RoutingTier:   routingTierNames[tier],
		WeeklyResetAt: weeklyReset,
		ObservedAt:    auth.Quota.ObservedAt,
		Usage:         usage,
	}
	if needs, reason := dashboard.NeedsRelogin(auth); needs {
		entry.NeedsRelogin = reason
	}
	for _, window := range coreauth.ParseQuotaWindows(auth.Provider, auth.Quota) {
		entry.Windows = append(entry.Windows, routingOverviewWindow{
			QuotaWindow:          window,
			ProjectedUsedAtReset: dashboard.ProjectWindowUse(window, now),
		})
	}
	if entry.Windows == nil {
		entry.Windows = []routingOverviewWindow{}
	}
	return entry
}

func routingOverviewPlan(auth *coreauth.Auth) string {
	for key, value := range auth.Quota.Signals {
		if strings.EqualFold(key, "X-Codex-Plan-Type") && strings.TrimSpace(value) != "" {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	for _, key := range []string{"plan_type", "subscription_type", "plan"} {
		if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
			return strings.ToLower(value)
		}
		if value, ok := auth.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.ToLower(strings.TrimSpace(value))
		}
	}
	if auth.AuthKind() == coreauth.AuthKindAPIKey {
		return "api-key"
	}
	return ""
}

// orderRoutingCandidates sorts one provider's credentials in the order the active strategy
// would try them: usable before unusable, higher priority first, then strategy order.
func orderRoutingCandidates(auths []*coreauth.Auth, entries map[string]*routingOverviewCredential, strategy string, now time.Time) {
	if strategy == coreauth.RoutingStrategyResetSoonest {
		coreauth.SortByResetSoonest(auths, now)
	} else {
		sort.SliceStable(auths, func(i, j int) bool { return auths[i].ID < auths[j].ID })
	}
	usable := func(auth *coreauth.Auth) bool {
		entry := entries[auth.ID]
		return !entry.Disabled && !entry.Unavailable && auth.Status != coreauth.StatusDisabled
	}
	sort.SliceStable(auths, func(i, j int) bool {
		if usableI, usableJ := usable(auths[i]), usable(auths[j]); usableI != usableJ {
			return usableI
		}
		return entries[auths[i].ID].Priority > entries[auths[j].ID].Priority
	})
}

func routingOverviewWarnings(strategy string, affinity bool, byProvider map[string][]*coreauth.Auth, stats dashboard.AffinityStats) []routingOverviewWarning {
	warnings := make([]routingOverviewWarning, 0)
	multiAccount := false
	for provider, auths := range byProvider {
		if !coreauth.ProviderSupportsQuotaObservation(provider) {
			continue
		}
		oauthCount := 0
		for _, auth := range auths {
			if auth.AuthKind() == coreauth.AuthKindOAuth && !auth.Disabled {
				oauthCount++
			}
		}
		if oauthCount > 1 {
			multiAccount = true
		}
	}
	if !affinity && multiAccount {
		message := "Session affinity is off, so requests from one conversation can rotate across subscription accounts. Prompt caches are per account, so each switch rebuilds the cache. Set routing.session-affinity: true to keep a session on one account."
		if stats.Switches > 0 {
			message += " Sessions have already switched accounts " + strconv.FormatInt(stats.Switches, 10) + " times since start-up."
		}
		warnings = append(warnings, routingOverviewWarning{Code: "session_affinity_disabled", Level: "warning", Message: message})
	}
	if strategy != coreauth.RoutingStrategyResetSoonest && multiAccount {
		warnings = append(warnings, routingOverviewWarning{
			Code:    "reset_soonest_available",
			Level:   "info",
			Message: "Traffic is spread with " + strategy + ". With several subscription accounts, routing.strategy: reset-soonest spends the quota that expires first, so less is left unused when a weekly window resets.",
		})
	}
	return warnings
}
