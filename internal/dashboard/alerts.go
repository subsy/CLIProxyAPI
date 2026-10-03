package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	alertKindQuotaWaste     = "quota_waste"
	alertKindQuotaExhausted = "quota_exhausted"
	alertKindRelogin        = "relogin"

	webhookFormatJSON  = "json"
	webhookFormatText  = "text"
	webhookFormatSlack = "slack"

	defaultQuotaWasteThreshold = 0.25
	defaultQuotaWasteWindow    = 24 * time.Hour
	alertEvaluateInterval      = time.Minute
	minutesPerDay              = 24 * 60
	// webhookTimeout bounds alert delivery; it is not upstream model traffic.
	webhookTimeout = 10 * time.Second
)

// Alert is a condition that needs the operator's attention.
type Alert struct {
	Kind     string    `json:"kind"`
	Key      string    `json:"key"`
	Provider string    `json:"provider"`
	AuthID   string    `json:"auth_id"`
	Account  string    `json:"account"`
	Title    string    `json:"title"`
	Message  string    `json:"message"`
	ResetAt  time.Time `json:"reset_at,omitempty"`
	Since    time.Time `json:"since"`
}

type alertSettings struct {
	webhookURL     string
	webhookFormat  string
	wasteThreshold float64
	wasteWindow    time.Duration
}

// Alerter evaluates credentials periodically and notifies once per alert condition.
type Alerter struct {
	mu       sync.Mutex
	settings alertSettings
	active   map[string]Alert
	// notified remembers delivered alerts until they expire so a condition is announced once.
	notified map[string]time.Time
	client   *http.Client
	now      func() time.Time
	// deliver posts one alert and reports whether the receiver accepted it.
	deliver func(alertSettings, Alert) error
}

var defaultAlerter = NewAlerter()

// DefaultAlerter returns the process-wide alerter.
func DefaultAlerter() *Alerter { return defaultAlerter }

// NewAlerter creates an alerter with default settings.
func NewAlerter() *Alerter {
	a := &Alerter{
		active:   make(map[string]Alert),
		notified: make(map[string]time.Time),
		client: &http.Client{
			Timeout: webhookTimeout,
			// Alerts carry account names; never let a receiver bounce them elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}
	a.deliver = a.sendWebhook
	a.Configure(config.RoutingAlertsConfig{})
	return a
}

// Configure applies routing.alerts settings; it is safe to call on every config reload.
func (a *Alerter) Configure(cfg config.RoutingAlertsConfig) {
	settings := alertSettings{
		webhookURL:     strings.TrimSpace(cfg.WebhookURL),
		webhookFormat:  webhookFormat(cfg.WebhookFormat, cfg.WebhookURL),
		wasteThreshold: defaultQuotaWasteThreshold,
		wasteWindow:    defaultQuotaWasteWindow,
	}
	if cfg.QuotaWasteThreshold != 0 {
		settings.wasteThreshold = cfg.QuotaWasteThreshold
	}
	if window := strings.TrimSpace(cfg.QuotaWasteWindow); window != "" {
		if parsed, errParse := time.ParseDuration(window); errParse == nil && parsed > 0 {
			settings.wasteWindow = parsed
		} else {
			log.Warnf("routing.alerts.quota-waste-window %q is not a valid duration; using %s", window, defaultQuotaWasteWindow)
		}
	}
	a.mu.Lock()
	a.settings = settings
	a.mu.Unlock()
}

// Start evaluates the credentials returned by list every minute until ctx is done.
func (a *Alerter) Start(ctx context.Context, list func() []*coreauth.Auth) {
	if a == nil || list == nil {
		return
	}
	go func() {
		a.Evaluate(list())
		ticker := time.NewTicker(alertEvaluateInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.Evaluate(list())
			}
		}
	}()
}

// Evaluate recomputes the active alerts and announces the ones not announced before.
func (a *Alerter) Evaluate(auths []*coreauth.Auth) {
	if a == nil {
		return
	}
	now := a.now()
	a.mu.Lock()
	settings := a.settings
	pools := summarizePools(auths, now)
	current := make(map[string]Alert)
	for _, auth := range auths {
		for _, alert := range alertsForAuth(auth, settings, pools, now) {
			if previous, ok := a.active[alert.Key]; ok {
				alert.Since = previous.Since
			}
			current[alert.Key] = alert
		}
	}
	// Expire first so a window that has reset can alert again in the same evaluation.
	for key, expiry := range a.notified {
		_, stillActive := current[key]
		// Alerts without a reset time (re-login) re-arm once the condition clears; quota alerts
		// stay quiet for the rest of their window even if the condition dips in and out.
		if (expiry.IsZero() && !stillActive) || (!expiry.IsZero() && now.After(expiry)) {
			delete(a.notified, key)
		}
	}
	var fresh []Alert
	for key, alert := range current {
		if _, done := a.notified[key]; !done {
			// Zero expiry means the alert stays announced until its condition clears.
			a.notified[key] = alert.ResetAt
			fresh = append(fresh, alert)
		}
	}
	a.active = current
	deliver := a.deliver
	a.mu.Unlock()

	for _, alert := range fresh {
		log.WithFields(log.Fields{"alert": alert.Kind, "provider": alert.Provider, "auth": alert.AuthID}).Warn(alert.Message)
		if settings.webhookURL != "" && deliver != nil {
			go a.deliverOnce(deliver, settings, alert)
		}
	}
}

// Active returns the alerts that currently hold, most urgent kind first.
func (a *Alerter) Active() []Alert {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	alerts := make([]Alert, 0, len(a.active))
	for _, alert := range a.active {
		alerts = append(alerts, alert)
	}
	a.mu.Unlock()
	sort.Slice(alerts, func(i, j int) bool {
		if alerts[i].Kind != alerts[j].Kind {
			return alertRank(alerts[i].Kind) < alertRank(alerts[j].Kind)
		}
		return alerts[i].Key < alerts[j].Key
	})
	return alerts
}

func alertRank(kind string) int {
	switch kind {
	case alertKindRelogin:
		return 0
	case alertKindQuotaExhausted:
		return 1
	default:
		return 2
	}
}

func alertsForAuth(auth *coreauth.Auth, settings alertSettings, pools map[string]poolSummary, now time.Time) []Alert {
	if auth == nil || auth.Disabled || auth.AuthKind() != coreauth.AuthKindOAuth {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	account := AccountLabel(auth)
	name := providerLabel(provider)
	newAlert := func(kind, key, title, message string, resetAt time.Time) Alert {
		return Alert{Kind: kind, Key: kind + ":" + auth.ID + key, Provider: provider, AuthID: auth.ID, Account: account,
			Title: title, Message: message, ResetAt: resetAt, Since: now}
	}
	// The reset time stays out of the key: Codex derives it from reset-after-seconds, so it
	// jitters between responses. Each announcement expires at its reset instead.
	windowKey := func(window coreauth.QuotaWindow) string { return ":" + window.Key }
	var alerts []Alert
	if needs, reason := NeedsRelogin(auth); needs {
		alerts = append(alerts, newAlert(alertKindRelogin, "", name+" account needs a new login",
			fmt.Sprintf("%s account %s can no longer refresh its login (%s). Sign in again from the management panel; until then it receives no traffic.", name, account, reason),
			time.Time{}))
	}
	windows := coreauth.ParseQuotaWindows(auth.Provider, auth.Quota)
	for _, window := range exhaustedWindows(windows, now) {
		title := fmt.Sprintf("%s subscription quota used up", name)
		message := fmt.Sprintf("%s account %s has used up its %s window. It is back in %s (at %s).",
			name, account, windowName(window), roundDuration(window.ResetAt.Sub(now)), clockTime(window.ResetAt, now))
		if pool := pools[provider]; pool.total > 1 && pool.available == 0 {
			title = fmt.Sprintf("All %s subscriptions are used up", name)
			message = fmt.Sprintf("%s account %s has used up its %s window, so no %s account has quota left. The first one is back in %s (at %s).",
				name, account, windowName(window), name, roundDuration(pool.nextBack.Sub(now)), clockTime(pool.nextBack, now))
		} else if pool.total > 1 {
			message += fmt.Sprintf(" %d of %d %s accounts still have quota.", pool.available, pool.total, name)
		}
		alerts = append(alerts, newAlert(alertKindQuotaExhausted, windowKey(window), title, message, window.ResetAt))
	}
	if settings.wasteThreshold <= 0 {
		return alerts
	}
	for _, window := range windows {
		if window.Limit != "" || window.WindowMinutes < minutesPerDay || !window.ResetAt.After(now) || window.ResetAt.Sub(now) > settings.wasteWindow {
			continue
		}
		projected := ProjectWindowUse(window, now)
		if projected == nil || 1-*projected < settings.wasteThreshold {
			continue
		}
		alerts = append(alerts, newAlert(alertKindQuotaWaste, windowKey(window), name+" quota about to expire unused",
			fmt.Sprintf("%s account %s has used %.0f%% of its %s window, which resets in %s. At the current pace about %.0f%% will expire unused, so this is a good time to start backlog work.",
				name, account, window.UsedFraction*100, window.Label, roundDuration(window.ResetAt.Sub(now)), (1-*projected)*100),
			window.ResetAt))
	}
	return alerts
}

// exhaustedWindows returns the credential's own quota windows that are used up and have not
// reset yet. Additional per-model limits (Codex Spark and similar) are left out.
func exhaustedWindows(windows []coreauth.QuotaWindow, now time.Time) []coreauth.QuotaWindow {
	var exhausted []coreauth.QuotaWindow
	for _, window := range windows {
		if window.Limit != "" || !window.ResetAt.After(now) {
			continue
		}
		if window.Exhausted() {
			exhausted = append(exhausted, window)
		}
	}
	return exhausted
}

// poolSummary describes how many subscription accounts of one provider can still serve.
type poolSummary struct {
	total     int
	available int
	// nextBack is the earliest time an exhausted account gets quota back.
	nextBack time.Time
}

func summarizePools(auths []*coreauth.Auth, now time.Time) map[string]poolSummary {
	pools := make(map[string]poolSummary)
	for _, auth := range auths {
		if auth == nil || auth.Disabled || auth.AuthKind() != coreauth.AuthKindOAuth {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		pool := pools[provider]
		pool.total++
		exhausted := exhaustedWindows(coreauth.ParseQuotaWindows(auth.Provider, auth.Quota), now)
		if needs, _ := NeedsRelogin(auth); !needs && len(exhausted) == 0 {
			pool.available++
		}
		// An account with several exhausted windows is back once the last of them resets.
		var back time.Time
		for _, window := range exhausted {
			if window.ResetAt.After(back) {
				back = window.ResetAt
			}
		}
		if !back.IsZero() && (pool.nextBack.IsZero() || back.Before(pool.nextBack)) {
			pool.nextBack = back
		}
		pools[provider] = pool
	}
	return pools
}

func windowName(window coreauth.QuotaWindow) string {
	if strings.Contains(window.Key, "_") {
		return strings.Replace(window.Key, "_", " (", 1) + ")"
	}
	return window.Label
}

func webhookFormat(format, url string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case webhookFormatText:
		return webhookFormatText
	case webhookFormatSlack:
		return webhookFormatSlack
	case webhookFormatJSON:
		return webhookFormatJSON
	case "":
		// Slack rejects payloads without a text field, so recognise its webhook host.
		if strings.Contains(strings.ToLower(url), "hooks.slack.com/") {
			return webhookFormatSlack
		}
		return webhookFormatJSON
	default:
		log.Warnf("routing.alerts.webhook-format %q is not json, text or slack; using json", format)
		return webhookFormatJSON
	}
}

// NeedsRelogin reports whether an OAuth credential has lost its login and why.
func NeedsRelogin(auth *coreauth.Auth) (bool, string) {
	if auth == nil || auth.AuthKind() != coreauth.AuthKindOAuth {
		return false, ""
	}
	message := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(auth.StatusMessage)), "_", " ")
	switch {
	case strings.Contains(message, "invalid grant"):
		return true, "the refresh token was rejected"
	case auth.Status == coreauth.StatusError && strings.Contains(message, "unauthorized"):
		return true, "the provider answered 401 Unauthorized"
	case auth.Status == coreauth.StatusError && strings.Contains(message, "token expired"):
		return true, "the access token expired and refreshing it failed"
	case auth.Status == coreauth.StatusError && auth.LastError != nil && auth.LastError.HTTPStatus == http.StatusUnauthorized:
		return true, "the provider answered 401 Unauthorized"
	case auth.LastError != nil && strings.Contains(strings.ToLower(auth.LastError.Message), "invalid_grant"):
		return true, "the refresh token was rejected"
	}
	return false, ""
}

// ProjectWindowUse extrapolates the share of a window that will be used by its reset if
// consumption continues at the average pace observed since the window started.
func ProjectWindowUse(window coreauth.QuotaWindow, now time.Time) *float64 {
	if window.WindowMinutes <= 0 || window.UsedFraction < 0 || window.ResetAt.IsZero() || !window.ResetAt.After(now) {
		return nil
	}
	length := time.Duration(window.WindowMinutes) * time.Minute
	elapsed := length - window.ResetAt.Sub(now)
	// Too little of the window has passed for the pace to mean anything.
	if elapsed < length/20 {
		return nil
	}
	projected := window.UsedFraction * float64(length) / float64(elapsed)
	return &projected
}

// AccountLabel returns the e-mail of a credential when known, otherwise its file name or ID.
func AccountLabel(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if email, ok := auth.Metadata["email"].(string); ok && strings.TrimSpace(email) != "" {
		return strings.TrimSpace(email)
	}
	if email := strings.TrimSpace(auth.Attributes["email"]); email != "" {
		return email
	}
	if name := strings.TrimSpace(auth.FileName); name != "" {
		return name
	}
	return auth.ID
}

func providerLabel(provider string) string {
	switch provider {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "":
		return "Provider"
	default:
		return strings.ToUpper(provider[:1]) + provider[1:]
	}
}

// roundDuration formats a wait for people: "3d 4h", "2h 5m", "45m" or "30s".
func roundDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	minutes := int(d.Round(time.Minute).Minutes())
	days, hours, mins := minutes/minutesPerDay, minutes/60%24, minutes%60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	default:
		return fmt.Sprintf("%dm", mins)
	}
}

// clockTime formats a time in the server's zone, adding the weekday when it is not today.
func clockTime(at, now time.Time) string {
	at, now = at.Local(), now.Local()
	if at.YearDay() != now.YearDay() || at.Year() != now.Year() {
		return at.Format("Mon 15:04 MST")
	}
	return at.Format("15:04 MST")
}

// deliverOnce posts an alert and, if delivery fails, forgets that it was announced so the
// next evaluation (a minute later) tries again while the condition still holds.
func (a *Alerter) deliverOnce(deliver func(alertSettings, Alert) error, settings alertSettings, alert Alert) {
	errDeliver := deliver(settings, alert)
	if errDeliver == nil {
		return
	}
	log.WithError(errDeliver).WithField("alert", alert.Kind).Warn("alerts: webhook delivery failed; retrying on the next evaluation")
	a.mu.Lock()
	delete(a.notified, alert.Key)
	a.mu.Unlock()
}

func (a *Alerter) sendWebhook(settings alertSettings, alert Alert) error {
	var body []byte
	contentType := "application/json"
	switch settings.webhookFormat {
	case webhookFormatText:
		body = []byte(alert.Message)
		contentType = "text/plain; charset=utf-8"
	default:
		var payload any = alert
		if settings.webhookFormat == webhookFormatSlack {
			payload = map[string]string{"text": "*" + alert.Title + "*\n" + alert.Message}
		}
		encoded, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			return fmt.Errorf("encode webhook payload: %w", errMarshal)
		}
		body = encoded
	}
	req, errReq := http.NewRequest(http.MethodPost, settings.webhookURL, bytes.NewReader(body))
	if errReq != nil {
		return fmt.Errorf("invalid webhook URL: %w", errReq)
	}
	req.Header.Set("Content-Type", contentType)
	// ntfy and similar services read these headers in text mode.
	req.Header.Set("Title", alert.Title)
	if alert.Kind == alertKindRelogin || alert.Kind == alertKindQuotaExhausted {
		req.Header.Set("Priority", "high")
	}
	resp, errDo := a.client.Do(req)
	if errDo != nil {
		return errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithError(errClose).Debug("alerts: closing webhook response body")
		}
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered HTTP %d", resp.StatusCode)
	}
	return nil
}
