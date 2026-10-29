package keylimit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type testClock struct{ t time.Time }

func (c *testClock) now() time.Time { return c.t }

func newTestLimiter(t *testing.T, limits config.APIKeyLimitsConfig, start time.Time) (*Limiter, *testClock) {
	t.Helper()
	if limits.StateFile == "" {
		limits.StateFile = filepath.Join(t.TempDir(), "state.json")
	}
	clock := &testClock{t: start}
	l := New()
	l.now = clock.now
	cfg := &config.Config{}
	cfg.APIKeyLimits = limits
	l.Configure(cfg)
	return l, clock
}

func charge(l *Limiter, key string, tokens int64) {
	l.HandleUsage(context.Background(), coreusage.Record{APIKey: key, Detail: coreusage.Detail{TotalTokens: tokens}})
}

func TestUnlimitedKeyIsNeverBlocked(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "full-key"}}}, time.Now())
	charge(l, "full-key", 1_000_000_000)
	status, tracked, limitErr := l.Check("full-key")
	if !tracked || limitErr != nil {
		t.Fatalf("tracked=%v err=%v, want tracked and allowed", tracked, limitErr)
	}
	if !status.Unlimited || status.UsedTokens != 1_000_000_000 {
		t.Fatalf("status = %+v, want unlimited with usage recorded", status)
	}
}

func TestUnknownKeyIsNotTracked(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "a"}}}, time.Now())
	if _, tracked, limitErr := l.Check("plain-api-key"); tracked || limitErr != nil {
		t.Fatalf("tracked=%v err=%v, want untracked", tracked, limitErr)
	}
}

func TestTokenLimitBlocksWhenExhausted(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "small", TokenLimit: 100}}}, time.Now())
	charge(l, "small", 60)
	status, _, limitErr := l.Check("small")
	if limitErr != nil || status.RemainingTokens != 40 {
		t.Fatalf("after 60 tokens: status=%+v err=%v", status, limitErr)
	}
	charge(l, "small", 50)
	status, _, limitErr = l.Check("small")
	if limitErr == nil || limitErr.Code != "api_key_token_limit_exceeded" {
		t.Fatalf("after 110 tokens: err=%v, want token limit error", limitErr)
	}
	if status.RemainingTokens != 0 || limitErr.RetryAfter != 0 {
		t.Fatalf("status=%+v retryAfter=%v, want 0 remaining and no reset for total period", status, limitErr.RetryAfter)
	}
}

func TestFallbackToInputPlusOutputTokens(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "k", TokenLimit: 100}}}, time.Now())
	l.HandleUsage(context.Background(), coreusage.Record{APIKey: "k", Detail: coreusage.Detail{InputTokens: 30, OutputTokens: 20}})
	if status, _, _ := l.Check("k"); status.UsedTokens != 50 {
		t.Fatalf("used = %d, want 50", status.UsedTokens)
	}
}

func TestSubscriptionLimitsAndOverrides(t *testing.T) {
	limits := config.APIKeyLimitsConfig{
		Subscriptions: []config.APIKeySubscription{{Name: "pro", TokenLimit: 1000, Period: "daily"}},
		Keys: []config.APIKeyLimit{
			{Key: "pro-user", Subscription: "pro"},
			{Key: "pro-plus", Subscription: "pro", TokenLimit: 5000},
			{Key: "pro-monthly", Subscription: "pro", Period: "monthly"},
		},
	}
	l, _ := newTestLimiter(t, limits, time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC))

	cases := map[string]struct {
		limit  int64
		period string
		reset  time.Time
	}{
		"pro-user":    {1000, "daily", time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)},
		"pro-plus":    {5000, "daily", time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)},
		"pro-monthly": {1000, "monthly", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)},
	}
	for key, want := range cases {
		status, tracked, limitErr := l.Check(key)
		if !tracked || limitErr != nil {
			t.Fatalf("%s: tracked=%v err=%v", key, tracked, limitErr)
		}
		if status.TokenLimit != want.limit || status.Period != want.period || !status.ResetsAt.Equal(want.reset) || status.Subscription != "pro" {
			t.Fatalf("%s: status=%+v, want limit=%d period=%s reset=%v", key, status, want.limit, want.period, want.reset)
		}
	}
}

func TestMisconfiguredKeysFailClosed(t *testing.T) {
	limits := config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{
		{Key: "ghost", Subscription: "missing"},
		{Key: "bad-period", TokenLimit: 10, Period: "fortnightly"},
	}}
	l, _ := newTestLimiter(t, limits, time.Now())
	for _, key := range []string{"ghost", "bad-period"} {
		_, tracked, limitErr := l.Check(key)
		if !tracked || limitErr == nil || limitErr.Code != "api_key_limit_misconfigured" {
			t.Fatalf("%s: tracked=%v err=%v, want misconfigured error", key, tracked, limitErr)
		}
	}
}

func TestDailyWindowResets(t *testing.T) {
	l, clock := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "d", TokenLimit: 100, Period: "daily"}}},
		time.Date(2026, 10, 29, 23, 0, 0, 0, time.UTC))
	charge(l, "d", 150)
	_, _, limitErr := l.Check("d")
	if limitErr == nil || limitErr.RetryAfter != time.Hour {
		t.Fatalf("err=%v, want exhaustion with 1h retry", limitErr)
	}
	clock.t = clock.t.Add(time.Hour)
	if status, _, limitErr := l.Check("d"); limitErr != nil || status.UsedTokens != 0 {
		t.Fatalf("next day: status=%+v err=%v, want fresh budget", status, limitErr)
	}
}

func TestWeeklyWindowStartsMonday(t *testing.T) {
	p, errParse := parsePeriod("weekly")
	if errParse != nil {
		t.Fatal(errParse)
	}
	sunday := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	if got, want := p.windowStart(sunday, time.Time{}), time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("windowStart = %v, want %v", got, want)
	}
}

func TestFixedDurationWindow(t *testing.T) {
	start := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	l, clock := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "f", TokenLimit: 100, Period: "5h"}}}, start)
	charge(l, "f", 100)
	clock.t = start.Add(4 * time.Hour)
	if _, _, limitErr := l.Check("f"); limitErr == nil || limitErr.RetryAfter != time.Hour {
		t.Fatalf("err=%v, want blocked with 1h left", limitErr)
	}
	clock.t = start.Add(5 * time.Hour)
	if status, _, limitErr := l.Check("f"); limitErr != nil || status.UsedTokens != 0 || !status.WindowStart.Equal(clock.t) {
		t.Fatalf("status=%+v err=%v, want new window at %v", status, limitErr, clock.t)
	}
}

func TestStatePersistsWithoutRawKeys(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "nested", "usage.json")
	limits := config.APIKeyLimitsConfig{StateFile: statePath, Keys: []config.APIKeyLimit{{Key: "secret-key-value", TokenLimit: 100}}}
	l, _ := newTestLimiter(t, limits, time.Now())
	charge(l, "secret-key-value", 70)
	l.Flush()

	data, errRead := os.ReadFile(statePath)
	if errRead != nil {
		t.Fatalf("read state: %v", errRead)
	}
	if strings.Contains(string(data), "secret-key-value") {
		t.Fatalf("state file leaks raw key: %s", data)
	}

	reloaded, _ := newTestLimiter(t, limits, time.Now())
	if status, _, _ := reloaded.Check("secret-key-value"); status.UsedTokens != 70 {
		t.Fatalf("reloaded used = %d, want 70", status.UsedTokens)
	}
}

func TestReconfigureKeepsCounters(t *testing.T) {
	limits := config.APIKeyLimitsConfig{
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		Keys:      []config.APIKeyLimit{{Key: "k", TokenLimit: 100}},
	}
	l, _ := newTestLimiter(t, limits, time.Now())
	charge(l, "k", 90)

	cfg := &config.Config{}
	cfg.APIKeyLimits = limits
	cfg.APIKeyLimits.Keys[0].TokenLimit = 200
	l.Configure(cfg)
	status, _, limitErr := l.Check("k")
	if limitErr != nil || status.UsedTokens != 90 || status.RemainingTokens != 110 {
		t.Fatalf("status=%+v err=%v, want counters kept across reload", status, limitErr)
	}
}

func TestSnapshotMasksKeys(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "sk-abcdefghijkl", Name: "alice"}}}, time.Now())
	snapshot := l.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Key != "sk-a...ijkl" || snapshot[0].Name != "alice" || snapshot[0].ID != hashKey("sk-abcdefghijkl")[:16] {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}
