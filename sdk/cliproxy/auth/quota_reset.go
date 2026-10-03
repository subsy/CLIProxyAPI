package auth

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// QuotaWindow is one subscription usage window parsed from passive quota signals.
type QuotaWindow struct {
	// Key identifies the window within its credential, e.g. "5h", "7d", "7d_oi",
	// "primary" or "<limit>:secondary".
	Key string `json:"key"`
	// Label is a short human readable window length such as "5h" or "7d".
	Label string `json:"label"`
	// Limit names an additional Codex limit; empty for the credential's main limits.
	Limit string `json:"limit,omitempty"`
	// WindowMinutes is the window length when known, otherwise 0.
	WindowMinutes int `json:"window_minutes,omitempty"`
	// UsedFraction is the consumed share of the window (1.0 = exhausted); -1 when unknown.
	UsedFraction float64 `json:"used_fraction"`
	// ResetAt is when the window resets, zero when unknown.
	ResetAt time.Time `json:"reset_at,omitempty"`
	// Status is the upstream window status when reported (Claude only).
	Status string `json:"status,omitempty"`
}

// Exhausted reports whether the window currently blocks requests. An upstream status wins:
// Claude reports utilization above 1.0 on windows it still allows. Without a status, a full
// window counts as exhausted unless it is a Claude per-model or overage window (key with a
// suffix such as "7d_oi"), which does not block the whole credential.
func (w QuotaWindow) Exhausted() bool {
	if w.Status != "" {
		return w.Status == "rejected"
	}
	if strings.Contains(w.Key, "_") {
		return false
	}
	return w.UsedFraction >= 1
}

const (
	quotaWindowDayMinutes  = 24 * 60
	quotaWindowWeekMinutes = 7 * quotaWindowDayMinutes
)

var claudeQuotaWindowID = regexp.MustCompile(`^(\d+)([hd])(_[a-z0-9]+)?$`)

// ParseQuotaWindows converts the passive quota signal snapshot of a credential into
// usage windows, ordered for display. Unknown providers and empty snapshots yield nil.
func ParseQuotaWindows(provider string, quota QuotaState) []QuotaWindow {
	windows := parseQuotaWindowsUnsorted(provider, quota)
	sort.Slice(windows, func(i, j int) bool {
		if windows[i].Limit != windows[j].Limit {
			return windows[i].Limit < windows[j].Limit
		}
		if windows[i].WindowMinutes != windows[j].WindowMinutes {
			return windows[i].WindowMinutes < windows[j].WindowMinutes
		}
		return windows[i].Key < windows[j].Key
	})
	return windows
}

func parseQuotaWindowsUnsorted(provider string, quota QuotaState) []QuotaWindow {
	if len(quota.Signals) == 0 {
		return nil
	}
	signals := make(map[string]string, len(quota.Signals))
	for key, value := range quota.Signals {
		signals[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude":
		return parseClaudeQuotaWindows(signals)
	case "codex":
		return parseCodexQuotaWindows(signals, quota.ObservedAt)
	default:
		return nil
	}
}

func parseClaudeQuotaWindows(signals map[string]string) []QuotaWindow {
	const prefix = "anthropic-ratelimit-unified-"
	byID := make(map[string]*QuotaWindow)
	for key, value := range signals {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		rest := strings.TrimPrefix(key, prefix)
		cut := strings.LastIndex(rest, "-")
		if cut <= 0 {
			continue
		}
		id, field := rest[:cut], rest[cut+1:]
		match := claudeQuotaWindowID.FindStringSubmatch(id)
		if match == nil {
			continue
		}
		window := byID[id]
		if window == nil {
			window = &QuotaWindow{Key: id, Label: id, UsedFraction: -1}
			if amount, errAtoi := strconv.Atoi(match[1]); errAtoi == nil {
				if match[2] == "h" {
					window.WindowMinutes = amount * 60
				} else {
					window.WindowMinutes = amount * 24 * 60
				}
				window.Label = match[1] + match[2]
			}
			byID[id] = window
		}
		switch field {
		case "utilization":
			if used, errParse := strconv.ParseFloat(value, 64); errParse == nil && used >= 0 {
				window.UsedFraction = used
			}
		case "reset":
			if resetAt, ok := parseQuotaUnixSeconds(value); ok {
				window.ResetAt = resetAt
			}
		case "status":
			window.Status = strings.ToLower(value)
		}
	}
	windows := make([]QuotaWindow, 0, len(byID))
	for _, window := range byID {
		windows = append(windows, *window)
	}
	return windows
}

func parseCodexQuotaWindows(signals map[string]string, observedAt time.Time) []QuotaWindow {
	const prefix = "x-codex-"
	suffixes := []string{"-window-minutes", "-used-percent", "-reset-at", "-reset-after-seconds"}
	byPrefix := make(map[string]*QuotaWindow)
	for key, value := range signals {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		var field string
		base := ""
		for _, suffix := range suffixes {
			if strings.HasSuffix(key, suffix) {
				field = suffix
				base = strings.TrimSuffix(key, suffix)
				break
			}
		}
		if field == "" {
			continue
		}
		namespace := strings.TrimPrefix(base, prefix)
		slot := ""
		switch {
		case namespace == "primary" || namespace == "secondary":
			slot, namespace = namespace, ""
		case strings.HasSuffix(namespace, "-primary"):
			slot, namespace = "primary", strings.TrimSuffix(namespace, "-primary")
		case strings.HasSuffix(namespace, "-secondary"):
			slot, namespace = "secondary", strings.TrimSuffix(namespace, "-secondary")
		default:
			continue
		}
		window := byPrefix[base]
		if window == nil {
			window = &QuotaWindow{Key: slot, UsedFraction: -1}
			if namespace != "" {
				window.Key = namespace + ":" + slot
				window.Limit = signals[prefix+namespace+"-limit-name"]
				if window.Limit == "" {
					window.Limit = namespace
				}
			}
			byPrefix[base] = window
		}
		switch field {
		case "-window-minutes":
			if minutes, errAtoi := strconv.Atoi(value); errAtoi == nil && minutes > 0 {
				window.WindowMinutes = minutes
			}
		case "-used-percent":
			if used, errParse := strconv.ParseFloat(value, 64); errParse == nil && used >= 0 {
				window.UsedFraction = used / 100
			}
		case "-reset-at":
			if resetAt, ok := parseQuotaUnixSeconds(value); ok {
				window.ResetAt = resetAt
			}
		case "-reset-after-seconds":
			// reset-at wins when both are present because it does not depend on observation time.
			if seconds, errParse := strconv.ParseFloat(value, 64); errParse == nil && seconds >= 0 && !observedAt.IsZero() && signals[base+"-reset-at"] == "" {
				window.ResetAt = observedAt.Add(time.Duration(seconds * float64(time.Second)))
			}
		}
	}
	windows := make([]QuotaWindow, 0, len(byPrefix))
	for _, window := range byPrefix {
		window.Label = quotaWindowLabel(window.WindowMinutes, window.Key)
		windows = append(windows, *window)
	}
	return windows
}

func quotaWindowLabel(minutes int, fallback string) string {
	switch {
	case minutes <= 0:
		return fallback
	case minutes%(24*60) == 0:
		return fmt.Sprintf("%dd", minutes/(24*60))
	case minutes%60 == 0:
		return fmt.Sprintf("%dh", minutes/60)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

func parseQuotaUnixSeconds(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if seconds, errParse := strconv.ParseFloat(raw, 64); errParse == nil && seconds > 0 {
		whole := int64(seconds)
		return time.Unix(whole, int64((seconds-float64(whole))*1e9)), true
	}
	if parsed, errParse := time.Parse(time.RFC3339, raw); errParse == nil {
		return parsed, true
	}
	return time.Time{}, false
}

// Reset-soonest ranking tiers, best first.
const (
	// ResetTierDiscover marks a subscription credential whose quota has not been observed
	// since start-up; one request teaches the proxy its reset time.
	ResetTierDiscover = iota
	// ResetTierKnown marks a credential with a known weekly reset and remaining quota.
	ResetTierKnown
	// ResetTierUnknown marks a credential without usable weekly window data.
	ResetTierUnknown
	// ResetTierExhausted marks a credential whose short window is nearly used up or whose
	// weekly windows are exhausted. The cooldown machinery still handles hard rejections.
	ResetTierExhausted
)

// resetSoonestShortWindowLimit demotes a credential before its short window rejects requests.
const resetSoonestShortWindowLimit = 0.98

// ResetSoonestRank returns the routing tier of a credential and, for ResetTierKnown,
// the weekly reset time used to order credentials within the tier.
func ResetSoonestRank(auth *Auth, now time.Time) (int, time.Time) {
	if auth == nil {
		return ResetTierUnknown, time.Time{}
	}
	return resetSoonestRankFromWindows(auth, parseQuotaWindowsUnsorted(auth.Provider, auth.Quota), now)
}

// resetSoonestRankFromWindows ranks a credential from windows parsed out of its own quota
// snapshot, so callers on the pick path can parse once per snapshot.
func resetSoonestRankFromWindows(auth *Auth, windows []QuotaWindow, now time.Time) (int, time.Time) {
	if auth == nil {
		return ResetTierUnknown, time.Time{}
	}
	if auth.Quota.ObservedAt.IsZero() {
		// Probe each credential once; one that answers without quota headers must not keep
		// winning picks, so it drops to unknown after its first completed request.
		if ProviderSupportsQuotaObservation(auth.Provider) && auth.AuthKind() == AuthKindOAuth && auth.Success+auth.Failed == 0 {
			return ResetTierDiscover, time.Time{}
		}
		return ResetTierUnknown, time.Time{}
	}
	var weeklyReset time.Time
	weeklyKnown, weeklyRemaining := false, false
	for _, window := range windows {
		if window.Limit != "" {
			continue
		}
		windowLength := time.Duration(window.WindowMinutes) * time.Minute
		resetAt := window.ResetAt
		expired := !resetAt.IsZero() && !resetAt.After(now)
		if !expired && window.Exhausted() {
			return ResetTierExhausted, time.Time{}
		}
		if window.WindowMinutes > 0 && window.WindowMinutes < quotaWindowDayMinutes {
			if !expired && window.UsedFraction >= resetSoonestShortWindowLimit {
				return ResetTierExhausted, time.Time{}
			}
			continue
		}
		// Per-model and overage windows do not decide the credential's own weekly reset.
		if window.WindowMinutes < quotaWindowWeekMinutes || resetAt.IsZero() || strings.Contains(window.Key, "_") {
			continue
		}
		weeklyKnown = true
		// A window that already rolled over has full quota again; its next reset is roughly
		// one window length after the observed one.
		for expired && !resetAt.After(now) && windowLength > 0 {
			resetAt = resetAt.Add(windowLength)
		}
		weeklyRemaining = true
		if weeklyReset.IsZero() || resetAt.Before(weeklyReset) {
			weeklyReset = resetAt
		}
	}
	switch {
	case weeklyRemaining:
		return ResetTierKnown, weeklyReset
	case weeklyKnown:
		return ResetTierExhausted, time.Time{}
	default:
		return ResetTierUnknown, time.Time{}
	}
}

type resetSoonestKey struct {
	tier    int
	resetAt time.Time
	id      string
}

func resetSoonestKeyFor(auth *Auth, now time.Time) resetSoonestKey {
	tier, resetAt := ResetSoonestRank(auth, now)
	return newResetSoonestKey(auth, tier, resetAt)
}

func newResetSoonestKey(auth *Auth, tier int, resetAt time.Time) resetSoonestKey {
	key := resetSoonestKey{tier: tier, resetAt: resetAt}
	if auth != nil {
		key.id = auth.ID
	}
	return key
}

func (k resetSoonestKey) less(other resetSoonestKey) bool {
	if k.tier != other.tier {
		return k.tier < other.tier
	}
	if k.tier == ResetTierKnown && !k.resetAt.Equal(other.resetAt) {
		return k.resetAt.Before(other.resetAt)
	}
	return k.id < other.id
}

// SortByResetSoonest orders credentials the way the reset-soonest strategy prefers them.
func SortByResetSoonest(auths []*Auth, now time.Time) {
	type ranked struct {
		auth *Auth
		key  resetSoonestKey
	}
	order := make([]ranked, len(auths))
	for i, auth := range auths {
		order[i] = ranked{auth: auth, key: resetSoonestKeyFor(auth, now)}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i].key.less(order[j].key) })
	for i := range order {
		auths[i] = order[i].auth
	}
}

func pickResetSoonest(auths []*Auth, now time.Time) *Auth {
	var best *Auth
	var bestKey resetSoonestKey
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		key := resetSoonestKeyFor(auth, now)
		if best == nil || key.less(bestKey) {
			best, bestKey = auth, key
		}
	}
	return best
}

// ResetSoonestSelector drains the credential whose weekly quota resets soonest first, so
// subscription quota that would expire unused at reset is spent before quota that lasts longer.
type ResetSoonestSelector struct{}

// Pick selects the available credential with the soonest weekly quota reset.
func (s *ResetSoonestSelector) Pick(ctx context.Context, provider, model string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	available, err := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return pickResetSoonest(available, now), nil
}
