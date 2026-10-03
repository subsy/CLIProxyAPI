package auth

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func unixString(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func claudeQuotaAuth(id string, observedAt time.Time, used5h, used7d string, reset7d time.Time) *Auth {
	return &Auth{
		ID:         id,
		Provider:   "claude",
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Quota: QuotaState{ObservedAt: observedAt, Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Utilization": used5h,
			"Anthropic-Ratelimit-Unified-5h-Reset":       unixString(observedAt.Add(2 * time.Hour)),
			"Anthropic-Ratelimit-Unified-7d-Utilization": used7d,
			"Anthropic-Ratelimit-Unified-7d-Reset":       unixString(reset7d),
			"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
		}},
	}
}

func TestParseQuotaWindowsClaude(t *testing.T) {
	now := time.Unix(1787279282, 0)
	auth := claudeQuotaAuth("a", now, "0.25", "0.53", now.Add(48*time.Hour))
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"] = "1.02"
	windows := ParseQuotaWindows("claude", auth.Quota)
	if len(windows) != 3 {
		t.Fatalf("windows = %#v, want 3", windows)
	}
	if windows[0].Key != "5h" || windows[0].WindowMinutes != 300 || windows[0].UsedFraction != 0.25 {
		t.Fatalf("5h window = %#v", windows[0])
	}
	if windows[1].Key != "7d" || windows[1].Status != "allowed" || !windows[1].ResetAt.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("7d window = %#v", windows[1])
	}
	if windows[2].Key != "7d_oi" || windows[2].Label != "7d" || windows[2].UsedFraction != 1.02 {
		t.Fatalf("7d_oi window = %#v", windows[2])
	}
}

func TestParseQuotaWindowsCodexClassifiesByWindowLength(t *testing.T) {
	observed := time.Unix(1787279282, 0)
	quota := QuotaState{ObservedAt: observed, Signals: map[string]string{
		"X-Codex-Primary-Used-Percent":               "48",
		"X-Codex-Primary-Window-Minutes":             "10080",
		"X-Codex-Primary-Reset-After-Seconds":        "3600",
		"X-Codex-Bengalfox-Limit-Name":               "GPT-5.3-Codex-Spark",
		"X-Codex-Bengalfox-Secondary-Used-Percent":   "35",
		"X-Codex-Bengalfox-Secondary-Window-Minutes": "300",
		"X-Codex-Bengalfox-Secondary-Reset-At":       unixString(observed.Add(time.Hour)),
	}}
	windows := ParseQuotaWindows("codex", quota)
	if len(windows) != 2 {
		t.Fatalf("windows = %#v, want 2", windows)
	}
	main := windows[0]
	if main.Key != "primary" || main.Label != "7d" || main.UsedFraction != 0.48 || !main.ResetAt.Equal(observed.Add(time.Hour)) {
		t.Fatalf("main window = %#v", main)
	}
	extra := windows[1]
	if extra.Limit != "GPT-5.3-Codex-Spark" || extra.Key != "bengalfox:secondary" || extra.Label != "5h" {
		t.Fatalf("additional window = %#v", extra)
	}
}

func rejectedWeekly(auth *Auth) *Auth {
	auth.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
	return auth
}

func TestResetSoonestRankTiers(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		auth *Auth
		tier int
	}{
		{"unobserved oauth", &Auth{ID: "x", Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}, ResetTierDiscover},
		{"unobserved api key", &Auth{ID: "x", Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindAPIKey}}, ResetTierUnknown},
		{"known", claudeQuotaAuth("x", now, "0.1", "0.5", now.Add(time.Hour)), ResetTierKnown},
		{"short window nearly full", claudeQuotaAuth("x", now, "0.99", "0.5", now.Add(time.Hour)), ResetTierExhausted},
		{"weekly exhausted", rejectedWeekly(claudeQuotaAuth("x", now, "0.1", "1.0", now.Add(time.Hour))), ResetTierExhausted},
		{"weekly rolled over", claudeQuotaAuth("x", now.Add(-8*24*time.Hour), "0.1", "1.0", now.Add(-time.Hour)), ResetTierKnown},
	}
	for _, tc := range cases {
		if tier, _ := ResetSoonestRank(tc.auth, now); tier != tc.tier {
			t.Errorf("%s: tier = %d, want %d", tc.name, tier, tc.tier)
		}
	}
}

func resetSoonestFixture(now time.Time) []*Auth {
	return []*Auth{
		claudeQuotaAuth("a-late", now, "0.1", "0.2", now.Add(96*time.Hour)),
		claudeQuotaAuth("b-soon", now, "0.1", "0.7", now.Add(20*time.Hour)),
		claudeQuotaAuth("c-mid", now, "0.1", "0.4", now.Add(50*time.Hour)),
		claudeQuotaAuth("d-burnt", now, "0.99", "0.1", now.Add(time.Hour)),
	}
}

func TestResetSoonestSelectorPick(t *testing.T) {
	now := time.Now()
	got, errPick := (&ResetSoonestSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, resetSoonestFixture(now))
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if got.ID != "b-soon" {
		t.Fatalf("Pick() = %s, want b-soon", got.ID)
	}

	withNew := append(resetSoonestFixture(now), &Auth{ID: "e-new", Provider: "claude", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}})
	got, _ = (&ResetSoonestSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, withNew)
	if got.ID != "e-new" {
		t.Fatalf("Pick() = %s, want unobserved e-new to be probed first", got.ID)
	}
}

func TestSortByResetSoonest(t *testing.T) {
	now := time.Now()
	auths := resetSoonestFixture(now)
	SortByResetSoonest(auths, now)
	want := []string{"b-soon", "c-mid", "a-late", "d-burnt"}
	for i, auth := range auths {
		if auth.ID != want[i] {
			t.Fatalf("order[%d] = %s, want %s", i, auth.ID, want[i])
		}
	}
}

func TestSchedulerPick_ResetSoonest(t *testing.T) {
	t.Parallel()
	now := time.Now()
	scheduler := newSchedulerForTest(&ResetSoonestSelector{}, resetSoonestFixture(now)...)
	if scheduler.strategy != schedulerStrategyResetSoonest {
		t.Fatalf("strategy = %v, want reset-soonest", scheduler.strategy)
	}
	got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickSingle() error = %v", errPick)
	}
	if got.ID != "b-soon" {
		t.Fatalf("pickSingle() = %s, want b-soon", got.ID)
	}
	got, errPick = scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, map[string]struct{}{"b-soon": {}})
	if errPick != nil {
		t.Fatalf("pickSingle() retry error = %v", errPick)
	}
	if got.ID != "c-mid" {
		t.Fatalf("pickSingle() retry = %s, want c-mid", got.ID)
	}
}

func TestSchedulerPickMixed_ResetSoonest(t *testing.T) {
	t.Parallel()
	now := time.Now()
	codexLate := &Auth{
		ID:         "codex-late",
		Provider:   "codex",
		Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		Quota: QuotaState{ObservedAt: now, Signals: map[string]string{
			"X-Codex-Primary-Used-Percent":   "10",
			"X-Codex-Primary-Window-Minutes": "10080",
			"X-Codex-Primary-Reset-At":       unixString(now.Add(200 * time.Hour)),
		}},
	}
	scheduler := newSchedulerForTest(&ResetSoonestSelector{}, codexLate, claudeQuotaAuth("claude-soon", now, "0.1", "0.1", now.Add(10*time.Hour)))
	got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"codex", "claude"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickMixed() error = %v", errPick)
	}
	if got.ID != "claude-soon" || provider != "claude" {
		t.Fatalf("pickMixed() = %s/%s, want claude-soon/claude", got.ID, provider)
	}
}

func TestResetSoonestRankReviewCases(t *testing.T) {
	now := time.Now()
	// One weekly window used up blocks the credential even when another still has quota.
	split := claudeQuotaAuth("split", now, "0.1", "1.0", now.Add(time.Hour))
	split.Quota.Signals["Anthropic-Ratelimit-Unified-7d-Status"] = "rejected"
	split.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"] = "0.2"
	split.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Reset"] = unixString(now.Add(48 * time.Hour))
	if tier, _ := ResetSoonestRank(split, now); tier != ResetTierExhausted {
		t.Errorf("split weekly windows: tier = %d, want exhausted", tier)
	}
	// Claude can report an allowed overage window above 100%; it must not demote the account.
	overage := claudeQuotaAuth("overage", now, "0.1", "0.5", now.Add(time.Hour))
	overage.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Utilization"] = "1.02"
	overage.Quota.Signals["Anthropic-Ratelimit-Unified-7d_oi-Reset"] = unixString(now.Add(time.Hour))
	if tier, _ := ResetSoonestRank(overage, now); tier != ResetTierKnown {
		t.Errorf("allowed overage window: tier = %d, want known", tier)
	}
	// A credential that answered without quota headers is probed once, then ranked unknown.
	silent := &Auth{ID: "silent", Provider: "claude", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}, Success: 1}
	if tier, _ := ResetSoonestRank(silent, now); tier != ResetTierUnknown {
		t.Errorf("credential without quota headers after a request: tier = %d, want unknown", tier)
	}
}

func TestSchedulerResetSoonestSeesQuotaFromOtherModels(t *testing.T) {
	now := time.Now()
	reg := registry.GetGlobalRegistry()
	for _, authID := range []string{"a", "b"} {
		reg.RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: "model-x"}, {ID: "model-y"}})
	}
	t.Cleanup(func() {
		reg.UnregisterClient("a")
		reg.UnregisterClient("b")
	})
	scheduler := newSchedulerForTest(&ResetSoonestSelector{},
		claudeQuotaAuth("a", now, "0.1", "0.2", now.Add(time.Hour)),
		claudeQuotaAuth("b", now, "0.1", "0.2", now.Add(2*time.Hour)),
	)
	for _, model := range []string{"model-x", "model-y"} {
		if got, _ := scheduler.pickSingle(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil); got == nil || got.ID != "a" {
			t.Fatalf("initial pick for %s = %v, want a", model, got)
		}
	}
	// A response for model-x reports that a's weekly window now resets next week.
	updated := claudeQuotaAuth("a", now, "0.1", "0.0", now.Add(7*24*time.Hour))
	updated.Generation = 1
	updated.UpdatedAt = now.Add(time.Second)
	scheduler.upsertAuthResult(updated, []string{"model-x"}, false)
	if got, _ := scheduler.pickSingle(context.Background(), "claude", "model-y", cliproxyexecutor.Options{}, nil); got == nil || got.ID != "b" {
		t.Fatalf("model-y pick after model-x observation = %v, want b", got)
	}
}
