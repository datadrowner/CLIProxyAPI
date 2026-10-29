package keylimit

import (
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// shareBucket is the time resolution of per-key account usage.
	shareBucket = 5 * time.Minute
	// shareRetention keeps usage long enough for weekly provider windows.
	shareRetention = 8 * 24 * time.Hour
	anonymousKeyID = "-"
)

// accountWindow is a subscription limit window reported by the provider for
// one upstream credential, e.g. Claude's 5-hour or 7-day utilization.
type accountWindow struct {
	UsedPercent float64       `json:"used_percent"`
	ResetsAt    time.Time     `json:"resets_at"`
	Length      time.Duration `json:"length"`
}

// accountState tracks one credential (by auth index): its windows and the
// usage weight each client key sent through it, in shareBucket buckets.
type accountState struct {
	Windows map[string]*accountWindow    `json:"windows"`
	Usage   map[string]map[int64]float64 `json:"usage"`
}

// AccountShare reports a key's portion of one account window.
type AccountShare struct {
	AuthIndex string `json:"auth_index"`
	Window    string `json:"window"`
	// UsedPercent is the key's estimated portion of the window, in percent of the account limit.
	UsedPercent float64 `json:"used_percent"`
	// SharePercent is the portion the key may use.
	SharePercent float64 `json:"share_percent"`
	// AccountUsedPercent is the window utilization reported by the provider.
	AccountUsedPercent float64   `json:"account_used_percent"`
	ResetsAt           time.Time `json:"resets_at"`
}

func (l *Limiter) accountLocked(authIndex string) *accountState {
	st := l.accounts[authIndex]
	if st == nil {
		st = &accountState{}
		l.accounts[authIndex] = st
	}
	if st.Windows == nil {
		st.Windows = make(map[string]*accountWindow)
	}
	if st.Usage == nil {
		st.Usage = make(map[string]map[int64]float64)
	}
	return st
}

// recordAccountUsageLocked stores provider windows and charges weight to the key.
func (l *Limiter) recordAccountUsageLocked(authIndex, keyID string, headers http.Header, weight float64, at, now time.Time) {
	st := l.accountLocked(authIndex)
	for name, w := range parseAccountWindows(headers, now) {
		w := w
		st.Windows[name] = &w
	}
	if weight <= 0 {
		return
	}
	if keyID == "" {
		keyID = anonymousKeyID
	}
	buckets := st.Usage[keyID]
	if buckets == nil {
		buckets = make(map[int64]float64)
		st.Usage[keyID] = buckets
	}
	buckets[at.Truncate(shareBucket).Unix()] += weight
	cutoff := now.Add(-shareRetention).Unix()
	for bucket := range buckets {
		if bucket < cutoff {
			delete(buckets, bucket)
		}
	}
}

func sumSince(buckets map[int64]float64, start int64) float64 {
	total := 0.0
	for bucket, weight := range buckets {
		if bucket >= start {
			total += weight
		}
	}
	return total
}

// accountSharesLocked estimates the key's portion of every active account window:
// the window utilization times the key's fraction of the weight sent through
// that account since the window started. Usage outside the proxy is therefore
// distributed across the keys in proportion to their use.
func (l *Limiter) accountSharesLocked(keyID string, share float64, now time.Time) []AccountShare {
	shares := []AccountShare{}
	for authIndex, st := range l.accounts {
		keyBuckets := st.Usage[keyID]
		if len(keyBuckets) == 0 {
			continue
		}
		for name, w := range st.Windows {
			if w == nil || w.Length <= 0 || !now.Before(w.ResetsAt) {
				continue
			}
			start := w.ResetsAt.Add(-w.Length).Truncate(shareBucket).Unix()
			keyWeight := sumSince(keyBuckets, start)
			if keyWeight <= 0 {
				continue
			}
			total := 0.0
			for _, buckets := range st.Usage {
				total += sumSince(buckets, start)
			}
			used := w.UsedPercent * math.Min(keyWeight/total, 1)
			shares = append(shares, AccountShare{
				AuthIndex:          authIndex,
				Window:             name,
				UsedPercent:        math.Round(used*100) / 100,
				SharePercent:       share,
				AccountUsedPercent: w.UsedPercent,
				ResetsAt:           w.ResetsAt,
			})
		}
	}
	sort.Slice(shares, func(i, j int) bool {
		if shares[i].AuthIndex != shares[j].AuthIndex {
			return shares[i].AuthIndex < shares[j].AuthIndex
		}
		return shares[i].Window < shares[j].Window
	})
	return shares
}

// parseAccountWindows reads provider rate-limit headers.
//
// Claude: anthropic-ratelimit-unified-<window>-utilization (0..1) and -reset
// (unix seconds); the window length comes from its name (5h, 7d, 7d_opus).
// Codex: x-codex-<limit>-used-percent (0..100), -window-minutes, and -reset-at
// (unix seconds) or -reset-after-seconds.
func parseAccountWindows(headers http.Header, now time.Time) map[string]accountWindow {
	if len(headers) == 0 {
		return nil
	}
	values := make(map[string]string, len(headers))
	for key, v := range headers {
		if len(v) > 0 {
			values[strings.ToLower(key)] = strings.TrimSpace(v[len(v)-1])
		}
	}
	number := func(key string) (float64, bool) {
		f, err := strconv.ParseFloat(values[key], 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	}
	unix := func(key string) time.Time {
		if f, ok := number(key); ok && f > 1e9 && f < 1e11 {
			return time.Unix(int64(f), 0).UTC()
		}
		return time.Time{}
	}
	windows := make(map[string]accountWindow)
	for key := range values {
		switch {
		case strings.HasPrefix(key, "anthropic-ratelimit-unified-") && strings.HasSuffix(key, "-utilization"):
			name := strings.TrimSuffix(strings.TrimPrefix(key, "anthropic-ratelimit-unified-"), "-utilization")
			used, ok := number(key)
			length := windowLength(name)
			resets := unix("anthropic-ratelimit-unified-" + name + "-reset")
			if !ok || length <= 0 || resets.IsZero() {
				continue
			}
			windows["claude "+name] = accountWindow{UsedPercent: clampPercent(used * 100), ResetsAt: resets, Length: length}
		case strings.HasPrefix(key, "x-codex-") && strings.HasSuffix(key, "-used-percent"):
			base := strings.TrimSuffix(key, "-used-percent")
			used, ok := number(key)
			minutes, okMinutes := number(base + "-window-minutes")
			resets := unix(base + "-reset-at")
			if resets.IsZero() {
				if after, okAfter := number(base + "-reset-after-seconds"); okAfter && after >= 0 && after < 1e8 {
					resets = now.Add(time.Duration(after) * time.Second).UTC()
				}
			}
			if !ok || !okMinutes || minutes <= 0 || resets.IsZero() {
				continue
			}
			windows["codex "+strings.TrimPrefix(base, "x-codex-")] = accountWindow{UsedPercent: clampPercent(used), ResetsAt: resets, Length: time.Duration(minutes) * time.Minute}
		}
	}
	return windows
}

// windowLength parses names like "5h", "7d" or "7d_opus".
func windowLength(name string) time.Duration {
	name, _, _ = strings.Cut(name, "_")
	if len(name) < 2 {
		return 0
	}
	n, err := strconv.Atoi(name[:len(name)-1])
	if err != nil || n <= 0 {
		return 0
	}
	switch name[len(name)-1] {
	case 'm':
		return time.Duration(n) * time.Minute
	case 'h':
		return time.Duration(n) * time.Hour
	case 'd':
		return time.Duration(n) * 24 * time.Hour
	}
	return 0
}

func clampPercent(p float64) float64 {
	return math.Max(0, math.Min(100, p))
}
