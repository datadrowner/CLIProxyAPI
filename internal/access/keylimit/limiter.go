// Package keylimit enforces per-client API key token budgets configured under
// access.api-key-limits. Consumed tokens are counted from usage records and
// persisted to a small JSON state file so budgets survive restarts.
package keylimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usageanalytics"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

// DefaultStateFileName is the state file name used inside the auth directory.
const DefaultStateFileName = "api-key-usage.json"

func init() {
	coreusage.RegisterNamedPlugin("api-key-limits", Default())
}

var defaultLimiter = New()

// Default returns the process-wide limiter.
func Default() *Limiter { return defaultLimiter }

// Status describes the current budget state of one configured key.
type Status struct {
	// ID is the first 16 hex characters of the key's SHA-256 digest; it lets
	// clients match a status to a key without exposing the key.
	ID string `json:"id"`
	// Key is a masked form of the API key.
	Key             string    `json:"key"`
	Name            string    `json:"name,omitempty"`
	Subscription    string    `json:"subscription,omitempty"`
	Period          string    `json:"period"`
	Unlimited       bool      `json:"unlimited"`
	TokenLimit      int64     `json:"token_limit,omitempty"`
	UsedTokens      int64     `json:"used_tokens"`
	RemainingTokens int64     `json:"remaining_tokens,omitempty"`
	Requests        int64     `json:"requests"`
	WindowStart     time.Time `json:"window_start,omitempty"`
	ResetsAt        time.Time `json:"resets_at,omitempty"`
	// AccountShare is the percentage of each connected account's windows the key may use.
	AccountShare float64 `json:"account_share,omitempty"`
	// AccountShares lists the key's estimated portion of each active account window.
	AccountShares []AccountShare `json:"account_shares,omitempty"`
}

// LimitError reports why a key may not start a new request.
type LimitError struct {
	Message string
	// Code is a stable machine-readable reason.
	Code string
	// RetryAfter is the time until the budget resets; zero when it never resets.
	RetryAfter time.Duration
}

func (e *LimitError) Error() string { return e.Message }

type policy struct {
	name         string
	subscription string
	limit        int64
	period       period
	accountShare float64
	// invalid holds a configuration error; such keys are rejected (fail closed).
	invalid string
}

type counter struct {
	Period      string    `json:"period"`
	WindowStart time.Time `json:"window_start,omitempty"`
	Tokens      int64     `json:"tokens"`
	Requests    int64     `json:"requests"`
}

type stateFile struct {
	// Keys maps the SHA-256 hex digest of each API key to its counter, so the
	// state file never contains the raw key.
	Keys map[string]*counter `json:"keys"`
	// Accounts maps upstream auth indexes to their windows and per-key usage.
	Accounts map[string]*accountState `json:"accounts,omitempty"`
}

// flushInterval bounds how long usage stays only in memory.
const flushInterval = 10 * time.Second

// Limiter tracks token consumption per configured client API key.
type Limiter struct {
	mu        sync.Mutex
	policies  map[string]policy
	counters  map[string]*counter
	accounts  map[string]*accountState
	statePath string
	dirty     bool

	saveMu    sync.Mutex
	flushOnce sync.Once
	now       func() time.Time
}

// New constructs an empty limiter.
func New() *Limiter {
	return &Limiter{
		policies: make(map[string]policy),
		counters: make(map[string]*counter),
		accounts: make(map[string]*accountState),
		now:      time.Now,
	}
}

// Configure replaces the key policies from cfg and loads persisted counters
// when the state file location changes.
func (l *Limiter) Configure(cfg *config.Config) {
	if l == nil {
		return
	}
	policies := make(map[string]policy)
	statePath := ""
	if cfg != nil {
		policies = buildPolicies(cfg.APIKeyLimits)
		if len(policies) > 0 {
			statePath = resolveStatePath(cfg)
		}
	}

	l.mu.Lock()
	pathChanged := statePath != l.statePath
	l.mu.Unlock()
	if pathChanged {
		l.Flush()
	}
	loaded := stateFile{}
	if pathChanged && statePath != "" {
		var errLoad error
		loaded, errLoad = loadState(statePath)
		if errLoad != nil {
			log.Warnf("api-key-limits: failed to load usage state from %s: %v", statePath, errLoad)
		}
	}

	l.mu.Lock()
	l.policies = policies
	if pathChanged {
		l.statePath = statePath
		l.counters = loaded.Keys
		if l.counters == nil {
			l.counters = make(map[string]*counter)
		}
		l.accounts = loaded.Accounts
		if l.accounts == nil {
			l.accounts = make(map[string]*accountState)
		}
		l.dirty = false
	}
	l.mu.Unlock()
	if statePath != "" {
		l.flushOnce.Do(func() { go l.flushLoop() })
	}
}

func (l *Limiter) flushLoop() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for range ticker.C {
		l.Flush()
	}
}

func buildPolicies(cfg config.APIKeyLimitsConfig) map[string]policy {
	subscriptions := make(map[string]config.APIKeySubscription, len(cfg.Subscriptions))
	for _, sub := range cfg.Subscriptions {
		name := strings.TrimSpace(sub.Name)
		if name == "" {
			continue
		}
		subscriptions[name] = sub
	}

	policies := make(map[string]policy, len(cfg.Keys))
	for _, entry := range cfg.Keys {
		key := strings.TrimSpace(entry.Key)
		if key == "" {
			continue
		}
		p := policy{
			name:         strings.TrimSpace(entry.Name),
			subscription: strings.TrimSpace(entry.Subscription),
		}
		rawPeriod := strings.TrimSpace(entry.Period)
		if p.subscription != "" {
			sub, ok := subscriptions[p.subscription]
			if !ok {
				p.invalid = fmt.Sprintf("unknown subscription %q", p.subscription)
				log.Errorf("api-key-limits: key %q references unknown subscription %q; the key is blocked", displayName(p, key), p.subscription)
				policies[key] = p
				continue
			}
			p.limit = sub.TokenLimit
			p.accountShare = sub.AccountShare
			if rawPeriod == "" {
				rawPeriod = strings.TrimSpace(sub.Period)
			}
		}
		if entry.TokenLimit > 0 {
			p.limit = entry.TokenLimit
		}
		if entry.AccountShare > 0 {
			p.accountShare = entry.AccountShare
		}
		if p.accountShare > 100 {
			p.invalid = fmt.Sprintf("account-share %.4g must be at most 100", p.accountShare)
			log.Errorf("api-key-limits: key %q has an invalid account share; the key is blocked", displayName(p, key))
		}
		if p.limit < 0 {
			p.limit = 0
		}
		parsed, errPeriod := parsePeriod(rawPeriod)
		if errPeriod != nil {
			p.invalid = errPeriod.Error()
			log.Errorf("api-key-limits: key %q has an invalid period: %v; the key is blocked", displayName(p, key), errPeriod)
		}
		p.period = parsed
		policies[key] = p
	}
	return policies
}

func resolveStatePath(cfg *config.Config) string {
	if path := strings.TrimSpace(cfg.APIKeyLimits.StateFile); path != "" {
		if resolved, errResolve := util.ResolveAuthDir(path); errResolve == nil && resolved != "" {
			return resolved
		}
		return path
	}
	authDir, errResolve := util.ResolveAuthDir(cfg.AuthDir)
	if errResolve != nil || strings.TrimSpace(authDir) == "" {
		authDir = "."
	}
	return filepath.Join(authDir, DefaultStateFileName)
}

// Check reports the key's current status. tracked is false when the key is not
// configured under api-key-limits. A non-nil error means the key must not start
// a new request.
func (l *Limiter) Check(apiKey string) (status Status, tracked bool, err *LimitError) {
	if l == nil {
		return Status{}, false, nil
	}
	apiKey = strings.TrimSpace(apiKey)
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.policies[apiKey]
	if !ok {
		return Status{}, false, nil
	}
	now := l.now()
	status = l.statusLocked(apiKey, p, now)
	if p.invalid != "" {
		return status, true, &LimitError{
			Message: "API key limit configuration is invalid: " + p.invalid,
			Code:    "api_key_limit_misconfigured",
		}
	}
	if !status.Unlimited && status.UsedTokens >= status.TokenLimit {
		limitErr := &LimitError{
			Message: fmt.Sprintf("API key token limit reached (%d/%d tokens", status.UsedTokens, status.TokenLimit),
			Code:    "api_key_token_limit_exceeded",
		}
		if !status.ResetsAt.IsZero() {
			limitErr.RetryAfter = status.ResetsAt.Sub(now)
			limitErr.Message += ", resets at " + status.ResetsAt.UTC().Format(time.RFC3339)
		}
		limitErr.Message += ")"
		return status, true, limitErr
	}
	var blocking *AccountShare
	for i := range status.AccountShares {
		s := &status.AccountShares[i]
		if s.UsedPercent >= s.SharePercent && (blocking == nil || s.ResetsAt.After(blocking.ResetsAt)) {
			blocking = s
		}
	}
	if blocking != nil {
		return status, true, &LimitError{
			Message: fmt.Sprintf("API key account share reached (%.1f%% of %.4g%% allowed in the %s window, resets at %s)",
				blocking.UsedPercent, blocking.SharePercent, blocking.Window, blocking.ResetsAt.UTC().Format(time.RFC3339)),
			Code:       "api_key_account_share_exceeded",
			RetryAfter: blocking.ResetsAt.Sub(now),
		}
	}
	return status, true, nil
}

// Snapshot returns the status of every configured key, sorted by masked key.
func (l *Limiter) Snapshot() []Status {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	out := make([]Status, 0, len(l.policies))
	for key, p := range l.policies {
		out = append(out, l.statusLocked(key, p, now))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// HandleUsage implements coreusage.Plugin. It charges consumed tokens to
// limited keys and records every request's weight per upstream account, so
// account shares can be split across all keys that used an account.
func (l *Limiter) HandleUsage(_ context.Context, record coreusage.Record) {
	if l == nil {
		return
	}
	apiKey := strings.TrimSpace(record.APIKey)
	authIndex := strings.TrimSpace(record.AuthIndex)
	weight := 0.0
	if authIndex != "" {
		weight = usageanalytics.Weight(record)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if l.statePath == "" {
		return
	}
	now := l.now()
	if authIndex != "" {
		at := record.RequestedAt
		if at.IsZero() {
			at = now
		}
		keyID := ""
		if apiKey != "" {
			keyID = hashKey(apiKey)[:16]
		}
		l.recordAccountUsageLocked(authIndex, keyID, record.ResponseHeaders, weight, at, now)
		l.dirty = true
	}
	p, ok := l.policies[apiKey]
	if apiKey == "" || !ok {
		return
	}
	c := l.counterLocked(apiKey, p, now)
	c.Tokens += recordTokens(record.Detail)
	if !record.Failed {
		c.Requests++
	}
	l.dirty = true
}

func recordTokens(detail coreusage.Detail) int64 {
	total := detail.TotalTokens
	if total <= 0 {
		total = detail.InputTokens + detail.OutputTokens
	}
	if total < 0 {
		return 0
	}
	return total
}

// counterLocked returns the key's counter, rolling it into the current window.
func (l *Limiter) counterLocked(apiKey string, p policy, now time.Time) *counter {
	id := hashKey(apiKey)
	c, ok := l.counters[id]
	if !ok || c == nil {
		c = &counter{}
		l.counters[id] = c
	}
	if c.Period != p.period.raw {
		// The period changed in config; start a fresh window.
		*c = counter{Period: p.period.raw, WindowStart: p.period.windowStart(now, time.Time{})}
		return c
	}
	if start := p.period.windowStart(now, c.WindowStart); !start.Equal(c.WindowStart) {
		c.WindowStart = start
		c.Tokens = 0
		c.Requests = 0
	}
	return c
}

func (l *Limiter) statusLocked(apiKey string, p policy, now time.Time) Status {
	c := l.counterLocked(apiKey, p, now)
	status := Status{
		ID:           hashKey(apiKey)[:16],
		Key:          maskKey(apiKey),
		Name:         p.name,
		Subscription: p.subscription,
		Period:       p.period.label(),
		Unlimited:    p.limit <= 0 && p.invalid == "",
		TokenLimit:   p.limit,
		UsedTokens:   c.Tokens,
		Requests:     c.Requests,
		WindowStart:  c.WindowStart,
		ResetsAt:     p.period.resetAt(c.WindowStart),
	}
	if !status.Unlimited {
		status.RemainingTokens = max(p.limit-c.Tokens, 0)
	}
	if p.accountShare > 0 {
		status.AccountShare = p.accountShare
		status.AccountShares = l.accountSharesLocked(status.ID, p.accountShare, now)
	}
	return status
}

// Flush writes pending usage to the state file.
func (l *Limiter) Flush() {
	if l == nil {
		return
	}
	l.mu.Lock()
	if !l.dirty || l.statePath == "" {
		l.mu.Unlock()
		return
	}
	data, errMarshal := json.Marshal(stateFile{Keys: l.counters, Accounts: l.accounts})
	path := l.statePath
	l.dirty = false
	l.mu.Unlock()
	if errMarshal != nil {
		log.Warnf("api-key-limits: failed to encode usage state: %v", errMarshal)
		return
	}

	l.saveMu.Lock()
	defer l.saveMu.Unlock()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		log.Warnf("api-key-limits: failed to create state directory: %v", errMkdir)
		return
	}
	tmp := path + ".tmp"
	if errWrite := os.WriteFile(tmp, data, 0o600); errWrite != nil {
		log.Warnf("api-key-limits: failed to write usage state: %v", errWrite)
		return
	}
	if errRename := os.Rename(tmp, path); errRename != nil {
		log.Warnf("api-key-limits: failed to replace usage state: %v", errRename)
	}
}

func loadState(path string) (stateFile, error) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			return stateFile{}, nil
		}
		return stateFile{}, errRead
	}
	var state stateFile
	if errUnmarshal := json.Unmarshal(data, &state); errUnmarshal != nil {
		return stateFile{}, errUnmarshal
	}
	return state, nil
}

func hashKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// displayName returns the configured label, or a masked form of the key.
func displayName(p policy, apiKey string) string {
	if p.name != "" {
		return p.name
	}
	return maskKey(apiKey)
}

func maskKey(apiKey string) string {
	if len(apiKey) <= 8 {
		return "key-" + hashKey(apiKey)[:8]
	}
	return apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
}
