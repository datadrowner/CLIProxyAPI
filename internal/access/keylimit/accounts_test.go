package keylimit

import (
	"context"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func claudeHeaders(utilization float64, reset time.Time) http.Header {
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-5h-Utilization", strconv.FormatFloat(utilization, 'f', -1, 64))
	h.Set("Anthropic-Ratelimit-Unified-5h-Reset", strconv.FormatInt(reset.Unix(), 10))
	return h
}

func opusRecord(key, authIndex string, at time.Time, headers http.Header) coreusage.Record {
	return coreusage.Record{
		APIKey: key, AuthIndex: authIndex, Model: "claude-opus-5-5", RequestedAt: at, ResponseHeaders: headers,
		Detail: coreusage.Detail{InputTokens: 100_000, TotalTokens: 100_000,
			TokenBreakdown: coreusage.NewIndependentTokenBreakdown(100_000, 0, 0, 0, 0, 100_000)},
	}
}

func TestAccountShareBlocksAndResetsWithAccountWindow(t *testing.T) {
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	limits := config.APIKeyLimitsConfig{
		Subscriptions: []config.APIKeySubscription{{Name: "share", AccountShare: 10}},
		Keys: []config.APIKeyLimit{
			{Key: "laptop", Subscription: "share"},
			{Key: "desktop", AccountShare: 30},
			{Key: "full"},
		},
	}
	l, clock := newTestLimiter(t, limits, start)
	reset := start.Add(2 * time.Hour)

	// laptop and full each send half of the account's traffic while it is at 16%.
	l.HandleUsage(context.Background(), opusRecord("laptop", "acc-1", start, claudeHeaders(0.10, reset)))
	l.HandleUsage(context.Background(), opusRecord("full", "acc-1", start, claudeHeaders(0.16, reset)))
	status, _, limitErr := l.Check("laptop")
	if limitErr != nil || len(status.AccountShares) != 1 {
		t.Fatalf("status=%+v err=%v", status, limitErr)
	}
	share := status.AccountShares[0]
	if share.AuthIndex != "acc-1" || share.Window != "claude 5h" || share.UsedPercent != 8 || share.SharePercent != 10 || share.AccountUsedPercent != 16 {
		t.Fatalf("share = %+v", share)
	}

	// The account climbs to 24%: laptop's half is 12% of the account, above its 10%.
	clock.t = start.Add(30 * time.Minute)
	l.HandleUsage(context.Background(), opusRecord("full", "acc-1", clock.t, claudeHeaders(0.24, reset)))
	l.HandleUsage(context.Background(), opusRecord("laptop", "acc-1", clock.t, nil))
	_, _, limitErr = l.Check("laptop")
	if limitErr == nil || limitErr.Code != "api_key_account_share_exceeded" || limitErr.RetryAfter != 90*time.Minute {
		t.Fatalf("err = %+v, want account share exceeded until the account resets", limitErr)
	}
	// desktop has a share but never used the account; full has no share.
	if _, _, errDesktop := l.Check("desktop"); errDesktop != nil {
		t.Fatalf("desktop blocked: %v", errDesktop)
	}
	if _, _, errFull := l.Check("full"); errFull != nil {
		t.Fatalf("full blocked: %v", errFull)
	}

	// When the account window resets, laptop may use the account again.
	clock.t = reset
	if status, _, errReset := l.Check("laptop"); errReset != nil || len(status.AccountShares) != 0 {
		t.Fatalf("after reset: status=%+v err=%v", status, errReset)
	}
}

func TestAccountShareSurvivesRestart(t *testing.T) {
	start := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	limits := config.APIKeyLimitsConfig{
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		Keys:      []config.APIKeyLimit{{Key: "laptop", AccountShare: 5}},
	}
	l, _ := newTestLimiter(t, limits, start)
	l.HandleUsage(context.Background(), opusRecord("laptop", "acc-1", start, claudeHeaders(0.08, start.Add(time.Hour))))
	l.Flush()

	reloaded, _ := newTestLimiter(t, limits, start.Add(time.Minute))
	if _, _, limitErr := reloaded.Check("laptop"); limitErr == nil {
		t.Fatal("account share usage lost across restart")
	}
}

func TestAccountShareValidation(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "k", AccountShare: 150}}}, time.Now())
	if _, _, limitErr := l.Check("k"); limitErr == nil || limitErr.Code != "api_key_limit_misconfigured" {
		t.Fatalf("err = %v, want misconfigured", limitErr)
	}
}

func TestParseAccountWindows(t *testing.T) {
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	h := http.Header{}
	h.Set("Anthropic-Ratelimit-Unified-7d_opus-Utilization", "0.5")
	h.Set("Anthropic-Ratelimit-Unified-7d_opus-Reset", strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10))
	h.Set("Anthropic-Ratelimit-Unified-Overage-Utilization", "0.9") // no length: ignored
	h.Set("X-Codex-Primary-Used-Percent", "37.5")
	h.Set("X-Codex-Primary-Window-Minutes", "300")
	h.Set("X-Codex-Primary-Reset-After-Seconds", "600")
	got := parseAccountWindows(h, now)
	if len(got) != 2 {
		t.Fatalf("windows = %+v", got)
	}
	if w := got["claude 7d_opus"]; w.UsedPercent != 50 || w.Length != 7*24*time.Hour || !w.ResetsAt.Equal(now.Add(48*time.Hour)) {
		t.Fatalf("claude = %+v", w)
	}
	if w := got["codex primary"]; w.UsedPercent != 37.5 || w.Length != 5*time.Hour || !w.ResetsAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("codex = %+v", w)
	}
}
