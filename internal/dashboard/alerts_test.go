package dashboard

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func oauthAuth(id, provider string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:         id,
		Provider:   provider,
		Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindOAuth},
		Metadata:   map[string]any{"email": id + "@example.com"},
	}
}

// wastefulClaude has used 10% of a week that is six days in, so ~88% would expire unused.
func wastefulClaude(id string, now time.Time) *coreauth.Auth {
	auth := oauthAuth(id, "claude")
	auth.Quota = coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.1",
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(now.Add(20*time.Hour).Unix(), 10),
	}}
	return auth
}

func newTestAlerter(now *time.Time) (*Alerter, chan Alert) {
	alerter := NewAlerter()
	alerter.now = func() time.Time { return *now }
	delivered := make(chan Alert, 16)
	alerter.deliver = func(_ alertSettings, alert Alert) { delivered <- alert }
	return alerter, delivered
}

func receiveAlert(t *testing.T, delivered chan Alert) Alert {
	t.Helper()
	select {
	case alert := <-delivered:
		return alert
	case <-time.After(5 * time.Second):
		t.Fatal("alert was not delivered")
		return Alert{}
	}
}

func TestAlerterAnnouncesQuotaWasteOncePerWindow(t *testing.T) {
	now := time.Now()
	alerter, _ := newTestAlerter(&now)
	alerter.Configure(config.RoutingAlertsConfig{WebhookURL: "http://example.invalid"})
	auths := []*coreauth.Auth{wastefulClaude("a", now)}

	alerter.Evaluate(auths)
	active := alerter.Active()
	if len(active) != 1 || active[0].Kind != alertKindQuotaWaste || !strings.Contains(active[0].Message, "expire unused") {
		t.Fatalf("active = %+v, want one quota waste alert", active)
	}
	if len(alerter.notified) != 1 {
		t.Fatalf("notified = %v, want one entry", alerter.notified)
	}
	firstSince := active[0].Since
	now = now.Add(time.Minute)
	alerter.Evaluate(auths)
	if got := alerter.Active(); len(got) != 1 || !got[0].Since.Equal(firstSince) {
		t.Fatalf("second evaluation = %+v, want the same alert kept", got)
	}
}

func TestAlerterSkipsWindowsOutsideHorizonOrBelowThreshold(t *testing.T) {
	now := time.Now()
	alerter, _ := newTestAlerter(&now)
	alerter.Configure(config.RoutingAlertsConfig{QuotaWasteWindow: "6h"})
	alerter.Evaluate([]*coreauth.Auth{wastefulClaude("a", now)})
	if got := alerter.Active(); len(got) != 0 {
		t.Fatalf("active = %+v, want none: reset is 20h away and the window is 6h", got)
	}
	alerter.Configure(config.RoutingAlertsConfig{QuotaWasteThreshold: -1})
	alerter.Evaluate([]*coreauth.Auth{wastefulClaude("a", now)})
	if got := alerter.Active(); len(got) != 0 {
		t.Fatalf("active = %+v, want none when quota alerts are disabled", got)
	}
}

func TestAlerterReloginRearmsAfterRecovery(t *testing.T) {
	now := time.Now()
	alerter, delivered := newTestAlerter(&now)
	alerter.Configure(config.RoutingAlertsConfig{WebhookURL: "http://example.invalid"})
	broken := oauthAuth("b", "codex")
	broken.Status = coreauth.StatusError
	broken.StatusMessage = "unauthorized"

	alerter.Evaluate([]*coreauth.Auth{broken})
	alerter.Evaluate([]*coreauth.Auth{broken})
	healthy := oauthAuth("b", "codex")
	healthy.Status = coreauth.StatusActive
	alerter.Evaluate([]*coreauth.Auth{healthy})
	if got := alerter.Active(); len(got) != 0 {
		t.Fatalf("active after recovery = %+v", got)
	}
	alerter.Evaluate([]*coreauth.Auth{broken})

	// One delivery per episode: the repeated evaluation while broken must not resend.
	for episode := 0; episode < 2; episode++ {
		if alert := receiveAlert(t, delivered); alert.Kind != alertKindRelogin {
			t.Fatalf("episode %d delivered %+v", episode, alert)
		}
	}
	if extra := len(delivered); extra != 0 {
		t.Fatalf("%d extra deliveries", extra)
	}
}

func TestNeedsRelogin(t *testing.T) {
	cases := []struct {
		status  coreauth.Status
		message string
		want    bool
	}{
		{coreauth.StatusError, "unauthorized", true},
		{coreauth.StatusError, "invalid grant (retrying)", true},
		{coreauth.StatusDisabled, "disabled (invalid grant)", true},
		{coreauth.StatusError, "token expired", true},
		{coreauth.StatusActive, "", false},
		{coreauth.StatusError, "rate limited", false},
	}
	for _, tc := range cases {
		auth := oauthAuth("x", "claude")
		auth.Status, auth.StatusMessage = tc.status, tc.message
		if got, _ := NeedsRelogin(auth); got != tc.want {
			t.Errorf("NeedsRelogin(%s, %q) = %v, want %v", tc.status, tc.message, got, tc.want)
		}
	}
	apiKey := &coreauth.Auth{Provider: "claude", Status: coreauth.StatusError, StatusMessage: "unauthorized",
		Attributes: map[string]string{coreauth.AttributeAuthKind: coreauth.AuthKindAPIKey}}
	if got, _ := NeedsRelogin(apiKey); got {
		t.Fatalf("API keys cannot be re-logged in and must not alert")
	}
}

func TestSendWebhookFormats(t *testing.T) {
	type received struct{ contentType, title, body string }
	got := make(chan received, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{r.Header.Get("Content-Type"), r.Header.Get("Title"), string(body)}
	}))
	defer server.Close()
	alerter := NewAlerter()
	alert := Alert{Kind: alertKindQuotaWaste, Title: "Claude quota about to expire unused", Message: "use it"}

	alerter.sendWebhook(alertSettings{webhookURL: server.URL}, alert)
	jsonReq := <-got
	if !strings.HasPrefix(jsonReq.contentType, "application/json") || !strings.Contains(jsonReq.body, `"kind":"quota_waste"`) {
		t.Fatalf("json webhook = %+v", jsonReq)
	}
	alerter.sendWebhook(alertSettings{webhookURL: server.URL, webhookFormat: webhookFormatText}, alert)
	textReq := <-got
	if textReq.body != "use it" || textReq.title != alert.Title {
		t.Fatalf("text webhook = %+v", textReq)
	}
	alerter.sendWebhook(alertSettings{webhookURL: server.URL, webhookFormat: webhookFormatSlack}, alert)
	slackReq := <-got
	if slackReq.body != `{"text":"*Claude quota about to expire unused*\nuse it"}` {
		t.Fatalf("slack webhook = %+v", slackReq)
	}
}

func TestWebhookFormatDetectsSlack(t *testing.T) {
	if got := webhookFormat("", "https://hooks.slack.com/services/T0/B0/xyz"); got != webhookFormatSlack {
		t.Fatalf("format for a Slack URL = %q", got)
	}
	if got := webhookFormat("", "https://ntfy.sh/topic"); got != webhookFormatJSON {
		t.Fatalf("default format = %q", got)
	}
	if got := webhookFormat("text", "https://hooks.slack.com/services/x"); got != webhookFormatText {
		t.Fatalf("explicit format must win, got %q", got)
	}
}

func exhaustedClaude(id string, now time.Time, reset time.Time) *coreauth.Auth {
	auth := oauthAuth(id, "claude")
	auth.Quota = coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0",
		"Anthropic-Ratelimit-Unified-5h-Status":      "rejected",
		"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(reset.Unix(), 10),
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.4",
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(now.Add(90*time.Hour).Unix(), 10),
	}}
	return auth
}

func TestAlerterAnnouncesQuotaExhaustion(t *testing.T) {
	now := time.Now()
	alerter, delivered := newTestAlerter(&now)
	alerter.Configure(config.RoutingAlertsConfig{WebhookURL: "http://example.invalid"})
	healthy := oauthAuth("ok", "claude")

	alerter.Evaluate([]*coreauth.Auth{exhaustedClaude("a", now, now.Add(2*time.Hour)), healthy})
	first := receiveAlert(t, delivered)
	if first.Kind != alertKindQuotaExhausted || !strings.Contains(first.Message, "used up its 5h window") || !strings.Contains(first.Message, "1 of 2 Claude accounts still have quota") {
		t.Fatalf("partial exhaustion alert = %+v", first)
	}

	alerter.Evaluate([]*coreauth.Auth{exhaustedClaude("a", now, now.Add(2*time.Hour)), exhaustedClaude("b", now, now.Add(time.Hour))})
	second := receiveAlert(t, delivered)
	if second.AuthID != "b" || second.Title != "All Claude subscriptions are used up" || !strings.Contains(second.Message, "so no Claude account has quota left. The first one is back in 1h (at ") {
		t.Fatalf("pool exhaustion alert = %+v", second)
	}

	// After the window resets, the condition clears and nothing more is sent.
	now = now.Add(3 * time.Hour)
	alerter.Evaluate([]*coreauth.Auth{exhaustedClaude("a", now.Add(-3*time.Hour), now.Add(-time.Hour))})
	if got := alerter.Active(); len(got) != 0 {
		t.Fatalf("active after reset = %+v", got)
	}
	if extra := len(delivered); extra != 0 {
		t.Fatalf("%d extra deliveries", extra)
	}
}

func TestRoundDuration(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:                  "30s",
		59*time.Minute + 59*time.Second:   "1h",
		2*time.Hour + 5*time.Minute:       "2h 5m",
		45 * time.Minute:                  "45m",
		3*24*time.Hour + 4*time.Hour + 10: "3d 4h",
	}
	for in, want := range cases {
		if got := roundDuration(in); got != want {
			t.Errorf("roundDuration(%s) = %q, want %q", in, got, want)
		}
	}
}
