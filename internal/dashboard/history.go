package dashboard

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// HistoryFileName is stored next to the config file. It must not end in ".json":
	// the auth directory is often the config directory and every *.json there is read as a credential.
	HistoryFileName = "quota-history.jsonl"

	historyRetention = 28 * 24 * time.Hour
	// historyMinGap throttles samples per credential; used fractions are cumulative within a
	// window, so a coarser sample loses timing detail but no usage.
	historyMinGap         = 5 * time.Minute
	historyRecordInterval = time.Minute
	historyCompactEvery   = 24 * time.Hour
)

// HistoryWindow is one long quota window of a credential at one observation.
type HistoryWindow struct {
	Key     string  `json:"k"`
	Used    float64 `json:"u"`
	ResetAt int64   `json:"r,omitempty"`
}

// HistorySample is the state of a credential's long quota windows at one observation.
type HistorySample struct {
	At      int64           `json:"t"`
	Windows []HistoryWindow `json:"w"`
}

// QuotaHistory keeps a rolling record of day-or-longer quota windows per credential so the
// dashboard can draw remaining quota over time. Only changes are recorded.
type QuotaHistory struct {
	// ioMu serializes Record calls so an append can't land between a compaction's snapshot
	// and its rename, which would drop the appended sample from the file.
	ioMu        sync.Mutex
	mu          sync.Mutex
	samples     map[string][]HistorySample
	path        string
	lastCompact time.Time
	now         func() time.Time
}

var defaultQuotaHistory = NewQuotaHistory()

// DefaultQuotaHistory returns the process-wide quota history.
func DefaultQuotaHistory() *QuotaHistory { return defaultQuotaHistory }

// NewQuotaHistory creates an empty in-memory history.
func NewQuotaHistory() *QuotaHistory {
	return &QuotaHistory{samples: make(map[string][]HistorySample), now: time.Now}
}

// HistoryPath returns the history file location for a config file path, or "" when unknown.
func HistoryPath(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), HistoryFileName)
}

// Start loads the history file, then records the credentials returned by list every minute
// until ctx is done. An empty path keeps the history in memory only.
func (h *QuotaHistory) Start(ctx context.Context, list func() []*coreauth.Auth, path string) {
	if h == nil || list == nil {
		return
	}
	h.mu.Lock()
	h.path = path
	h.mu.Unlock()
	if errLoad := h.load(); errLoad != nil {
		log.Warnf("quota history: failed to load %s: %v", path, errLoad)
	}
	go func() {
		h.Record(list())
		ticker := time.NewTicker(historyRecordInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.Record(list())
			}
		}
	}()
}

// Record appends a sample for every credential whose long windows changed since its last sample.
func (h *QuotaHistory) Record(auths []*coreauth.Auth) {
	if h == nil {
		return
	}
	h.ioMu.Lock()
	defer h.ioMu.Unlock()
	now := h.now()
	type pending struct {
		id     string
		sample HistorySample
	}
	var added []pending
	h.mu.Lock()
	for _, auth := range auths {
		if auth == nil || auth.Quota.ObservedAt.IsZero() {
			continue
		}
		sample, ok := historySample(auth)
		if !ok {
			continue
		}
		existing := h.samples[auth.ID]
		if n := len(existing); n > 0 && sameWindows(existing[n-1].Windows, sample.Windows) {
			continue
		}
		merged, changed := mergeSample(existing, sample)
		if !changed {
			continue
		}
		h.samples[auth.ID] = merged
		added = append(added, pending{auth.ID, sample})
	}
	path := h.path
	compact := now.Sub(h.lastCompact) >= historyCompactEvery
	if compact {
		h.pruneLocked(now)
		h.lastCompact = now
	}
	h.mu.Unlock()

	if path == "" {
		return
	}
	if compact {
		if errSave := h.save(); errSave != nil {
			log.Warnf("quota history: failed to write %s: %v", path, errSave)
		}
		return
	}
	if len(added) == 0 {
		return
	}
	var buf []byte
	for _, p := range added {
		buf = appendHistoryLine(buf, p.id, p.sample)
	}
	if errAppend := appendFile(path, buf); errAppend != nil {
		log.Warnf("quota history: failed to append to %s: %v", path, errAppend)
	}
}

// Snapshot returns a copy of the recorded samples per credential ID.
func (h *QuotaHistory) Snapshot() map[string][]HistorySample {
	out := make(map[string][]HistorySample)
	if h == nil {
		return out
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, samples := range h.samples {
		out[id] = append([]HistorySample(nil), samples...)
	}
	return out
}

// mergeSample adds a newer sample, keeping about one sample per historyMinGap: while the
// previous sample is less than the gap after the one before it, the newer reading replaces
// it. A new window (reset) always starts its own sample. Record and load share this rule so
// replaying the appended file rebuilds the same samples.
func mergeSample(samples []HistorySample, sample HistorySample) ([]HistorySample, bool) {
	n := len(samples)
	if n > 0 && sample.At <= samples[n-1].At {
		return samples, false
	}
	if n >= 2 && time.Duration(sample.At-samples[n-2].At)*time.Second < historyMinGap && sameResets(samples[n-1].Windows, sample.Windows) {
		samples[n-1] = sample
		return samples, true
	}
	return append(samples, sample), true
}

// historySample captures the day-or-longer windows of a credential with a known usage.
func historySample(auth *coreauth.Auth) (HistorySample, bool) {
	sample := HistorySample{At: auth.Quota.ObservedAt.Unix()}
	for _, window := range coreauth.ParseQuotaWindows(auth.Provider, auth.Quota) {
		if window.WindowMinutes < minutesPerDay || window.UsedFraction < 0 {
			continue
		}
		entry := HistoryWindow{Key: window.Key, Used: window.UsedFraction}
		if !window.ResetAt.IsZero() {
			entry.ResetAt = window.ResetAt.Unix()
		}
		sample.Windows = append(sample.Windows, entry)
	}
	sort.Slice(sample.Windows, func(i, j int) bool { return sample.Windows[i].Key < sample.Windows[j].Key })
	return sample, len(sample.Windows) > 0
}

// resetTolerance absorbs reset times derived from a relative "resets in" value, which shift
// by a few seconds between observations of the same window.
const resetTolerance = 2 * 60

func sameReset(a, b HistoryWindow) bool {
	diff := a.ResetAt - b.ResetAt
	return a.Key == b.Key && diff <= resetTolerance && diff >= -resetTolerance
}

func sameWindows(a, b []HistoryWindow) bool {
	if !sameResets(a, b) {
		return false
	}
	for i := range a {
		if a[i].Used != b[i].Used {
			return false
		}
	}
	return true
}

func sameResets(a, b []HistoryWindow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameReset(a[i], b[i]) {
			return false
		}
	}
	return true
}

func (h *QuotaHistory) pruneLocked(now time.Time) {
	cutoff := now.Add(-historyRetention).Unix()
	for id, samples := range h.samples {
		keep := sort.Search(len(samples), func(i int) bool { return samples[i].At >= cutoff })
		// Keep the last sample before the cutoff: it is the state at the start of the range.
		if keep > 0 {
			keep--
		}
		if keep >= len(samples)-1 && len(samples) > 0 && samples[len(samples)-1].At < cutoff {
			delete(h.samples, id)
			continue
		}
		h.samples[id] = append([]HistorySample(nil), samples[keep:]...)
	}
}

// File format: one JSON object per line, {"id":"...","t":unix,"w":[{"k":"7d","u":0.4,"r":unix}]}.

func appendHistoryLine(dst []byte, id string, sample HistorySample) []byte {
	doc := ojson.NewDocString("{}")
	_ = doc.Set("id", id)
	_ = doc.Set("t", sample.At)
	_ = doc.SetRaw("w", "[]")
	for i, window := range sample.Windows {
		prefix := "w." + strconv.Itoa(i) + "."
		_ = doc.Set(prefix+"k", window.Key)
		_ = doc.Set(prefix+"u", window.Used)
		if window.ResetAt != 0 {
			_ = doc.Set(prefix+"r", window.ResetAt)
		}
	}
	return append(doc.AppendTo(dst), '\n')
}

func (h *QuotaHistory) load() error {
	h.mu.Lock()
	path := h.path
	h.mu.Unlock()
	if path == "" {
		return nil
	}
	file, errOpen := os.Open(path)
	if errOpen != nil {
		if os.IsNotExist(errOpen) {
			return nil
		}
		return errOpen
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.Warnf("quota history: failed to close %s: %v", path, errClose)
		}
	}()
	loaded := make(map[string][]HistorySample)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !ojson.Valid(line) {
			continue
		}
		root := ojson.Parse(line)
		id := root.Get("id").String()
		sample := HistorySample{At: root.Get("t").Int()}
		for _, w := range root.Get("w").Array() {
			sample.Windows = append(sample.Windows, HistoryWindow{Key: w.Get("k").String(), Used: w.Get("u").Float(), ResetAt: w.Get("r").Int()})
		}
		if id == "" || sample.At == 0 || len(sample.Windows) == 0 {
			continue
		}
		loaded[id], _ = mergeSample(loaded[id], sample)
	}
	if errScan := scanner.Err(); errScan != nil {
		return errScan
	}
	h.mu.Lock()
	h.samples = loaded
	h.mu.Unlock()
	return nil
}

func (h *QuotaHistory) save() error {
	h.mu.Lock()
	path := h.path
	ids := make([]string, 0, len(h.samples))
	for id := range h.samples {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var buf []byte
	for _, id := range ids {
		for _, sample := range h.samples[id] {
			buf = appendHistoryLine(buf, id, sample)
		}
	}
	h.mu.Unlock()
	tmp, errCreate := os.CreateTemp(filepath.Dir(path), HistoryFileName+".tmp-*")
	if errCreate != nil {
		return errCreate
	}
	tmpName := tmp.Name()
	_, errWrite := tmp.Write(buf)
	errClose := tmp.Close()
	if errWrite == nil {
		errWrite = errClose
	}
	if errWrite == nil {
		errWrite = os.Rename(tmpName, path)
	}
	if errWrite != nil {
		if errRemove := os.Remove(tmpName); errRemove != nil && !os.IsNotExist(errRemove) {
			log.Warnf("quota history: failed to remove %s: %v", tmpName, errRemove)
		}
	}
	return errWrite
}

func appendFile(path string, data []byte) (err error) {
	file, errOpen := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return errOpen
	}
	defer func() {
		if errClose := file.Close(); errClose != nil && err == nil {
			err = errClose
		}
	}()
	_, err = file.Write(data)
	return err
}
