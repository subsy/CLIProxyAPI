// Package dashboard aggregates in-memory usage statistics and serves the routing dashboard page.
package dashboard

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	// maxTrackedSessions bounds the per-session table; the least recently seen session is evicted.
	maxTrackedSessions = 2000
	// switchWarnInterval rate-limits the account-switch warning when session affinity is off.
	switchWarnInterval = 10 * time.Minute
)

// CredentialUsage is the accumulated usage of one credential since start-up.
type CredentialUsage struct {
	Requests            int64     `json:"requests"`
	Failed              int64     `json:"failed"`
	InputTokens         int64     `json:"input_tokens"`
	OutputTokens        int64     `json:"output_tokens"`
	CacheReadTokens     int64     `json:"cache_read_tokens"`
	CacheCreationTokens int64     `json:"cache_creation_tokens"`
	LastUsed            time.Time `json:"last_used,omitempty"`
}

// AffinityStats summarises how sessions stayed on, or moved between, credentials.
type AffinityStats struct {
	SessionsTracked int64 `json:"sessions_tracked"`
	// SessionsSwitched counts tracked sessions that were served by more than one credential.
	SessionsSwitched int64 `json:"sessions_switched"`
	// Switches counts requests whose credential differed from the session's previous request.
	Switches int64 `json:"switches"`
	// CacheCreationAfterSwitchTokens counts prompt-cache writes on the first request after a
	// switch: the cost of rebuilding a cache that already existed on another credential.
	CacheCreationAfterSwitchTokens int64 `json:"cache_creation_after_switch_tokens"`
	CacheReadTokens                int64 `json:"cache_read_tokens"`
	CacheCreationTokens            int64 `json:"cache_creation_tokens"`
}

type sessionState struct {
	authID   string
	switched bool
	lastSeen time.Time
}

// Stats is a usage plugin that aggregates per-credential and per-session usage in memory.
type Stats struct {
	mu           sync.Mutex
	credentials  map[string]*CredentialUsage
	sessions     map[string]*sessionState
	affinity     AffinityStats
	lastWarnedAt time.Time
	now          func() time.Time
}

var (
	defaultStats           = NewStats()
	sessionAffinityEnabled atomic.Bool
)

func init() {
	coreusage.RegisterNamedPlugin("routing-dashboard", defaultStats)
}

// DefaultStats returns the process-wide statistics collector.
func DefaultStats() *Stats { return defaultStats }

// SetSessionAffinityEnabled records whether routing.session-affinity is active, so account
// switches can be reported as avoidable.
func SetSessionAffinityEnabled(enabled bool) { sessionAffinityEnabled.Store(enabled) }

// SessionAffinityEnabled reports the last value passed to SetSessionAffinityEnabled.
func SessionAffinityEnabled() bool { return sessionAffinityEnabled.Load() }

// NewStats creates an empty collector.
func NewStats() *Stats {
	return &Stats{
		credentials: make(map[string]*CredentialUsage),
		sessions:    make(map[string]*sessionState),
		now:         time.Now,
	}
}

// HandleUsage implements coreusage.Plugin.
func (s *Stats) HandleUsage(_ context.Context, record coreusage.Record) {
	authID := strings.TrimSpace(record.AuthID)
	if s == nil || authID == "" {
		return
	}
	at := record.RequestedAt
	if at.IsZero() {
		at = s.now()
	}
	detail := record.Detail

	s.mu.Lock()
	defer s.mu.Unlock()
	usage := s.credentials[authID]
	if usage == nil {
		usage = &CredentialUsage{}
		s.credentials[authID] = usage
	}
	usage.Requests++
	if record.Failed {
		usage.Failed++
	}
	usage.InputTokens += detail.InputTokens
	usage.OutputTokens += detail.OutputTokens
	usage.CacheReadTokens += detail.CacheReadTokens
	usage.CacheCreationTokens += detail.CacheCreationTokens
	if at.After(usage.LastUsed) {
		usage.LastUsed = at
	}

	sessionID := strings.TrimSpace(record.SessionID)
	if sessionID == "" || record.Failed {
		return
	}
	s.affinity.CacheReadTokens += detail.CacheReadTokens
	s.affinity.CacheCreationTokens += detail.CacheCreationTokens
	session := s.sessions[sessionID]
	if session == nil {
		s.evictOldestSessionLocked()
		s.sessions[sessionID] = &sessionState{authID: authID, lastSeen: at}
		return
	}
	session.lastSeen = at
	if session.authID == authID {
		return
	}
	previous := session.authID
	session.authID = authID
	s.affinity.Switches++
	s.affinity.CacheCreationAfterSwitchTokens += detail.CacheCreationTokens
	if !session.switched {
		session.switched = true
		s.affinity.SessionsSwitched++
	}
	if now := s.now(); !SessionAffinityEnabled() && now.Sub(s.lastWarnedAt) >= switchWarnInterval {
		s.lastWarnedAt = now
		log.WithFields(log.Fields{
			"from_auth":             previous,
			"to_auth":               authID,
			"cache_creation_tokens": detail.CacheCreationTokens,
		}).Warn("routing: a session moved to another credential and its prompt cache had to be rebuilt; enable routing.session-affinity to keep sessions on one account")
	}
}

// evictOldestSessionLocked drops the least recently seen tenth of the sessions once the table
// is full, so the scan runs once per batch of new sessions rather than on every insert.
func (s *Stats) evictOldestSessionLocked() {
	if len(s.sessions) < maxTrackedSessions {
		return
	}
	seen := make([]time.Time, 0, len(s.sessions))
	for _, session := range s.sessions {
		seen = append(seen, session.lastSeen)
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i].Before(seen[j]) })
	cutoff := seen[maxTrackedSessions/10]
	for id, session := range s.sessions {
		if session.lastSeen.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
	// Equal timestamps can keep the table full; fall back to dropping arbitrary entries.
	for id := range s.sessions {
		if len(s.sessions) < maxTrackedSessions {
			break
		}
		delete(s.sessions, id)
	}
}

// Snapshot returns copies of the per-credential usage and the affinity summary.
func (s *Stats) Snapshot() (map[string]CredentialUsage, AffinityStats) {
	if s == nil {
		return nil, AffinityStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credentials := make(map[string]CredentialUsage, len(s.credentials))
	for id, usage := range s.credentials {
		credentials[id] = *usage
	}
	affinity := s.affinity
	affinity.SessionsTracked = int64(len(s.sessions))
	return credentials, affinity
}
