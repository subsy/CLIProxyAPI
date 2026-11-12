package dashboard

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func claudeAt(id string, observed time.Time, used string, reset time.Time) *coreauth.Auth {
	auth := oauthAuth(id, "claude")
	auth.Quota = coreauth.QuotaState{ObservedAt: observed, Signals: map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.5",
		"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(observed.Add(time.Hour).Unix(), 10),
		"Anthropic-Ratelimit-Unified-7d-Utilization": used,
		"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(reset.Unix(), 10),
	}}
	return auth
}

func TestQuotaHistoryRecordsChangesOnly(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	reset := start.Add(48 * time.Hour)
	h := NewQuotaHistory()

	at := func(minutes int, used string, reset time.Time) {
		h.Record([]*coreauth.Auth{claudeAt("a", start.Add(time.Duration(minutes)*time.Minute), used, reset)})
	}
	h.Record([]*coreauth.Auth{claudeAt("a", start, "0.2", reset), oauthAuth("no-quota", "xai")})
	at(1, "0.2", reset) // unchanged: skipped
	at(2, "0.25", reset)
	// Steady use: readings less than the gap after the sample before last replace the last one.
	at(3, "0.3", reset)
	at(4, "0.35", reset)
	at(6, "0.4", reset) // 6 minutes after the sample at 0: kept
	at(7, "0.45", reset)
	// A new window is kept even inside the gap.
	at(8, "0.01", reset.Add(7*24*time.Hour))

	got := h.Snapshot()
	if _, ok := got["no-quota"]; ok {
		t.Fatalf("credential without quota windows was recorded")
	}
	samples := got["a"]
	want := []float64{0.2, 0.35, 0.45, 0.01}
	if len(samples) != len(want) {
		t.Fatalf("got %d samples, want %d: %+v", len(samples), len(want), samples)
	}
	for i, sample := range samples {
		if len(sample.Windows) != 1 || sample.Windows[0].Key != "7d" {
			t.Fatalf("sample %d: want only the 7d window, got %+v", i, sample.Windows)
		}
		if sample.Windows[0].Used != want[i] {
			t.Fatalf("sample %d: used %v, want %v", i, sample.Windows[0].Used, want[i])
		}
	}
}

func TestQuotaHistoryPersistsAcrossRestart(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	reset := start.Add(48 * time.Hour)
	path := filepath.Join(t.TempDir(), HistoryFileName)
	clock := start

	first := NewQuotaHistory()
	first.now = func() time.Time { return clock }
	first.path = path
	first.lastCompact = start
	first.Record([]*coreauth.Auth{claudeAt("a", start, "0.2", reset)})
	first.Record([]*coreauth.Auth{claudeAt("a", start.Add(2*time.Minute), "0.3", reset)})
	// Replaces the reading at 2 minutes in memory but is appended to the file; reload must match.
	first.Record([]*coreauth.Auth{claudeAt("a", start.Add(3*time.Minute), "0.35", reset)})
	first.Record([]*coreauth.Auth{claudeAt("a", start.Add(20*time.Minute), "0.5", reset)})

	second := NewQuotaHistory()
	second.path = path
	if errLoad := second.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}
	samples := second.Snapshot()["a"]
	if len(samples) != 3 || samples[1].Windows[0].Used != 0.35 || samples[2].Windows[0].Used != 0.5 {
		t.Fatalf("unexpected samples after reload: %+v", samples)
	}
	if samples[2].Windows[0].ResetAt != reset.Unix() {
		t.Fatalf("reset not preserved: %+v", samples[2])
	}
}

func TestQuotaHistoryCompactionPrunesOldSamples(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	path := filepath.Join(t.TempDir(), HistoryFileName)
	clock := start
	h := NewQuotaHistory()
	h.now = func() time.Time { return clock }
	h.path = path
	h.lastCompact = start

	h.Record([]*coreauth.Auth{claudeAt("a", start, "0.1", start.Add(time.Hour))})
	h.Record([]*coreauth.Auth{claudeAt("a", start.Add(time.Hour), "0.2", start.Add(8*24*time.Hour))})
	h.Record([]*coreauth.Auth{claudeAt("old", start, "0.9", start.Add(time.Hour))})
	clock = start.Add(historyRetention + 2*time.Hour)
	h.Record([]*coreauth.Auth{claudeAt("a", clock, "0.3", clock.Add(time.Hour))})

	samples := h.Snapshot()
	if _, ok := samples["old"]; ok {
		t.Fatalf("credential with only expired samples was kept")
	}
	// The newest sample before the cutoff stays as the starting state.
	if got := samples["a"]; len(got) != 2 || got[0].Windows[0].Used != 0.2 {
		t.Fatalf("unexpected samples after compaction: %+v", got)
	}
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read: %v", errRead)
	}
	reloaded := NewQuotaHistory()
	reloaded.path = path
	if errLoad := reloaded.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}
	if got := reloaded.Snapshot(); len(got) != 1 || len(got["a"]) != 2 {
		t.Fatalf("compacted file does not match memory: %q", data)
	}
}

func TestHistoryPath(t *testing.T) {
	if got := HistoryPath(""); got != "" {
		t.Fatalf("empty config path gave %q", got)
	}
	if got := HistoryPath(filepath.Join("cfg", "config.yaml")); got != filepath.Join("cfg", HistoryFileName) {
		t.Fatalf("got %q", got)
	}
}

func TestQuotaHistoryIgnoresDriftingResetTimes(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	reset := start.Add(48 * time.Hour)
	h := NewQuotaHistory()
	h.Record([]*coreauth.Auth{claudeAt("a", start, "0.2", reset)})
	h.Record([]*coreauth.Auth{claudeAt("a", start.Add(time.Minute), "0.2", reset.Add(3*time.Second))})
	h.Record([]*coreauth.Auth{claudeAt("a", start.Add(2*time.Minute), "0.3", reset.Add(5*time.Second))})
	h.Record([]*coreauth.Auth{claudeAt("a", start.Add(3*time.Minute), "0.4", reset.Add(7*time.Second))})
	samples := h.Snapshot()["a"]
	if len(samples) != 2 || samples[1].Windows[0].Used != 0.4 {
		t.Fatalf("drifting reset should neither add an unchanged sample nor split the throttle gap: %+v", samples)
	}
}

func TestHistoryLineEscapesAndRoundTrips(t *testing.T) {
	sample := HistorySample{At: 1_800_000_000, Windows: []HistoryWindow{{Key: "7d", Used: 0.25, ResetAt: 1_800_100_000}, {Key: "7d_opus", Used: 1}}}
	line := appendHistoryLine(nil, `odd"id`, sample)
	path := filepath.Join(t.TempDir(), HistoryFileName)
	if errWrite := os.WriteFile(path, line, 0o600); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	h := NewQuotaHistory()
	h.path = path
	if errLoad := h.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}
	got := h.Snapshot()[`odd"id`]
	if len(got) != 1 || !sameWindows(got[0].Windows, sample.Windows) || got[0].At != sample.At {
		t.Fatalf("round trip of %s gave %+v", line, got)
	}
}

func TestQuotaHistoryRewritesFileAfterFailedWrite(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	reset := start.Add(48 * time.Hour)
	dir := t.TempDir()
	path := filepath.Join(dir, "missing", HistoryFileName)
	clock := start
	h := NewQuotaHistory()
	h.now = func() time.Time { return clock }
	h.path = path
	h.lastCompact = start

	// The directory doesn't exist yet, so this append fails and the sample stays in memory only.
	h.Record([]*coreauth.Auth{claudeAt("a", start, "0.2", reset)})
	if errMkdir := os.Mkdir(filepath.Join(dir, "missing"), 0o700); errMkdir != nil {
		t.Fatalf("mkdir: %v", errMkdir)
	}
	// Unchanged reading: nothing new to append, but the failed sample must still reach the file.
	clock = start.Add(time.Minute)
	h.Record([]*coreauth.Auth{claudeAt("a", start.Add(time.Minute), "0.2", reset)})

	reloaded := NewQuotaHistory()
	reloaded.path = path
	if errLoad := reloaded.load(); errLoad != nil {
		t.Fatalf("load: %v", errLoad)
	}
	if got := reloaded.Snapshot()["a"]; len(got) != 1 || got[0].Windows[0].Used != 0.2 {
		t.Fatalf("sample from the failed append was not written later: %+v", got)
	}
}
