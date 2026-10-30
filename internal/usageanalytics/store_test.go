package usageanalytics

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func claudeDetail(input, cacheRead, cacheWrite, output int64) coreusage.Detail {
	total := input + cacheRead + cacheWrite + output
	return coreusage.Detail{
		InputTokens:         input,
		CacheReadTokens:     cacheRead,
		CacheCreationTokens: cacheWrite,
		OutputTokens:        output,
		TotalTokens:         total,
		TokenBreakdown:      coreusage.NewIndependentTokenBreakdown(input, cacheRead, cacheWrite, output, 0, total),
	}
}

func almost(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestLookupPrice(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5-5":                        "claude-opus-5-5",
		"claude-opus-5":                          "claude-opus-5",
		"claude-opus-4-5-20251101":               "claude-opus-4-5",
		"claude-opus-4-20250514":                 "claude-opus-4",
		"claude-opus-4-1-20250805":               "claude-opus-4-1",
		"teamA/claude-sonnet-4-5-20250929":       "claude-sonnet-4-5",
		"us.anthropic.claude-haiku-4-5-20251001": "claude-haiku-4-5",
		"claude-sonnet-4-6(high)":                "claude-sonnet-4-6",
		"claude-3-5-haiku-latest":                "claude-3-5-haiku",
		"claude-opus-4-5@20251101":               "claude-opus-4-5",
	}
	for model, want := range cases {
		p, ok := LookupPrice(model)
		if !ok || p.Model != want {
			t.Fatalf("LookupPrice(%q) = %q, %v; want %q", model, p.Model, ok, want)
		}
	}
	for _, model := range []string{"gpt-5.4", "gemini-3-pro", "claude-opus-45"} {
		if _, ok := LookupPrice(model); ok {
			t.Fatalf("LookupPrice(%q) matched", model)
		}
	}
}

func TestCost(t *testing.T) {
	opus, _ := LookupPrice("claude-opus-5-5")
	// 1M uncached input ($4) + 1M cache read ($0.20) + 1M cache write ($5) + 1M output ($20).
	got := cost(opus, tokens{Input: 1_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000, Output: 1_000_000}, false)
	if !almost(got, 29.20) {
		t.Fatalf("opus cost = %v, want 29.20", got)
	}
	if fast := cost(opus, tokens{Input: 1_000_000}, true); !almost(fast, 8) {
		t.Fatalf("fast opus cost = %v, want 8", fast)
	}
	sonnet, _ := LookupPrice("claude-sonnet-4-6")
	if fast := cost(sonnet, tokens{Input: 1_000_000}, true); !almost(fast, 3) {
		t.Fatalf("sonnet ignores fast mode: got %v", fast)
	}
	haiku, _ := LookupPrice("claude-haiku-5-5")
	if short := cost(haiku, tokens{Input: 100_000, Output: 1_000_000}, false); !almost(short, 0.01+0.50) {
		t.Fatalf("short haiku = %v", short)
	}
	if long := cost(haiku, tokens{Input: 90_000, CacheRead: 20_000, Output: 1_000_000}, false); !almost(long, 0.045+0.001+2.50) {
		t.Fatalf("long haiku = %v", long)
	}
}

func TestStoreAggregatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.AuthDir = dir
	cfg.APIKeys = []string{"sk-main-key-123456"}
	cfg.APIKeyLimits.Keys = []config.APIKeyLimit{{Key: "sk-alice-key-99999", Name: "alice"}}

	s := New()
	s.now = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC) }
	s.Configure(cfg)
	at := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		s.HandleUsage(context.Background(), coreusage.Record{APIKey: "sk-alice-key-99999", AuthIndex: "a1", Provider: "claude", Model: "claude-opus-5-5", RequestedAt: at, Detail: claudeDetail(1_000_000, 0, 0, 0)})
	}
	s.HandleUsage(context.Background(), coreusage.Record{APIKey: "sk-main-key-123456", AuthIndex: "c1", Provider: "codex", Model: "gpt-5.4", RequestedAt: at, Detail: claudeDetail(500, 0, 0, 500), Failed: true})
	s.HandleUsage(context.Background(), coreusage.Record{APIKey: "sk-alice-key-99999", Model: "claude-opus-5-5", RequestedAt: time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC), Detail: claudeDetail(10, 0, 0, 0)})

	report := s.Report("")
	if report.Month != "2026-10" || len(report.Months) != 2 || report.Months[0] != "2026-10" {
		t.Fatalf("months = %s %v", report.Month, report.Months)
	}
	if len(report.Rows) != 2 {
		t.Fatalf("rows = %+v", report.Rows)
	}
	alice := report.Rows[0]
	if alice.KeyID != KeyID("sk-alice-key-99999") || alice.Requests != 2 || !almost(alice.CostUSD, 8) || alice.Provider != "claude" {
		t.Fatalf("alice row = %+v", alice)
	}
	codex := report.Rows[1]
	if codex.CostUSD != 0 || codex.UnpricedTokens != 1000 || codex.Failed != 1 {
		t.Fatalf("codex row = %+v", codex)
	}
	if report.Keys[alice.KeyID] != "alice" || report.Keys[codex.KeyID] != "sk-m...3456" {
		t.Fatalf("labels = %v", report.Keys)
	}

	s.Flush()
	data, errRead := os.ReadFile(filepath.Join(dir, DefaultStateFileName))
	if errRead != nil {
		t.Fatal(errRead)
	}
	if strings.Contains(string(data), "sk-alice") || strings.Contains(string(data), "sk-main") {
		t.Fatalf("state leaks raw keys: %s", data)
	}
	reloaded := New()
	reloaded.Configure(cfg)
	if rows := reloaded.Report("2026-10").Rows; len(rows) != 2 {
		t.Fatalf("reloaded rows = %+v", rows)
	}
}

func TestStoreRecordsDaysAndTrend(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.AuthDir = dir
	s := New()
	s.Configure(cfg)
	record := func(at time.Time, model string, input int64) {
		s.HandleUsage(context.Background(), coreusage.Record{APIKey: "k", Model: model, RequestedAt: at, Detail: claudeDetail(input, 0, 0, 0)})
	}
	record(time.Date(2026, 10, 3, 23, 30, 0, 0, time.UTC), "claude-opus-5-5", 1_000_000)
	record(time.Date(2026, 10, 3, 1, 0, 0, 0, time.UTC), "gpt-5.4", 200)
	record(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), "claude-opus-5-5", 500_000)
	record(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), "claude-opus-5-5", 250_000)

	report := s.Report("2026-10")
	if len(report.Days) != 2 || report.Days[0].Period != "2026-10-03" || report.Days[1].Period != "2026-10-07" {
		t.Fatalf("days = %+v", report.Days)
	}
	if d := report.Days[0]; d.Requests != 2 || d.TotalTokens != 1_000_200 || !almost(d.CostUSD, 4) || d.UnpricedTokens != 200 {
		t.Fatalf("2026-10-03 = %+v", d)
	}
	if len(report.Trend) != 2 || report.Trend[0].Period != "2026-09" || !almost(report.Trend[0].CostUSD, 1) || report.Trend[1].Period != "2026-10" || !almost(report.Trend[1].CostUSD, 6) {
		t.Fatalf("trend = %+v", report.Trend)
	}

	s.Flush()
	reloaded := New()
	reloaded.Configure(cfg)
	if days := reloaded.Report("2026-10").Days; len(days) != 2 || days[1].TotalTokens != 500_000 {
		t.Fatalf("reloaded days = %+v", days)
	}

	// Days of pruned months are dropped with them.
	for m := 1; m <= retainedMonths; m++ {
		record(time.Date(2027, time.Month(m), 1, 0, 0, 0, 0, time.UTC), "claude-opus-5-5", 1)
	}
	if days := s.Report("2026-10").Days; len(days) != 0 {
		t.Fatalf("days of pruned months kept: %+v", days)
	}
}
