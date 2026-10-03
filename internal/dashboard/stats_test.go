package dashboard

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestStatsTracksCredentialUsageAndSessionSwitches(t *testing.T) {
	stats := NewStats()
	at := time.Unix(1787279282, 0)
	record := func(authID, sessionID string, read, write int64) {
		stats.HandleUsage(context.Background(), coreusage.Record{
			AuthID: authID, SessionID: sessionID, RequestedAt: at,
			Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 5, CacheReadTokens: read, CacheCreationTokens: write},
		})
	}
	record("a", "s1", 0, 1000)
	record("a", "s1", 900, 100)
	record("b", "s1", 0, 1200) // switch: the cache is rebuilt on b
	record("b", "s2", 0, 300)
	stats.HandleUsage(context.Background(), coreusage.Record{AuthID: "a", SessionID: "s2", Failed: true})

	credentials, affinity := stats.Snapshot()
	if got := credentials["a"]; got.Requests != 3 || got.Failed != 1 || got.CacheReadTokens != 900 || got.CacheCreationTokens != 1100 {
		t.Fatalf("credential a = %+v", got)
	}
	if got := credentials["b"]; got.Requests != 2 || got.InputTokens != 20 || !got.LastUsed.Equal(at) {
		t.Fatalf("credential b = %+v", got)
	}
	if affinity.SessionsTracked != 2 || affinity.Switches != 1 || affinity.SessionsSwitched != 1 {
		t.Fatalf("affinity = %+v, want 2 sessions, 1 switch", affinity)
	}
	if affinity.CacheCreationAfterSwitchTokens != 1200 || affinity.CacheReadTokens != 900 || affinity.CacheCreationTokens != 2600 {
		t.Fatalf("affinity cache tokens = %+v", affinity)
	}
}

func TestStatsBoundsTrackedSessions(t *testing.T) {
	stats := NewStats()
	base := time.Unix(1787279282, 0)
	for i := 0; i < maxTrackedSessions+5; i++ {
		stats.HandleUsage(context.Background(), coreusage.Record{
			AuthID: "a", SessionID: "s" + string(rune('A'+i%26)) + time.Duration(i).String(), RequestedAt: base.Add(time.Duration(i) * time.Second),
		})
	}
	_, affinity := stats.Snapshot()
	if affinity.SessionsTracked > maxTrackedSessions || affinity.SessionsTracked < maxTrackedSessions*9/10 {
		t.Fatalf("sessions tracked = %d, want between %d and %d", affinity.SessionsTracked, maxTrackedSessions*9/10, maxTrackedSessions)
	}
}
