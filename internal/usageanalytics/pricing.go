package usageanalytics

import (
	"strings"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

// PricingSource documents where the price table comes from.
const PricingSource = "Anthropic API list prices (platform.claude.com/docs/en/about-claude/pricing), retrieved 2026-10-29; USD per million tokens"

// Price holds Anthropic first-party API list prices in USD per million tokens.
type Price struct {
	Model        string  `json:"model"`
	Name         string  `json:"name"`
	Input        float64 `json:"input"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
	CacheRead    float64 `json:"cache_read"`
	Output       float64 `json:"output"`
	// FastMode reports whether usage.speed "fast" doubles every price category.
	FastMode bool `json:"fast_mode,omitempty"`
	// LongPrompt applies to prompts above LongPromptThreshold input tokens.
	LongPromptThreshold int64  `json:"long_prompt_threshold,omitempty"`
	LongPrompt          *Price `json:"long_prompt,omitempty"`
	// Legacy marks retired models priced from their last published list price.
	Legacy bool `json:"legacy,omitempty"`
}

// Prices lists model ID prefixes; the longest matching prefix wins.
var Prices = []Price{
	{Model: "claude-fable-5-1", Name: "Claude Fable 5.1", Input: 10, CacheWrite5m: 12.50, CacheWrite1h: 20, CacheRead: 0.25, Output: 50},
	{Model: "claude-mythos-5-1", Name: "Claude Mythos 5.1", Input: 10, CacheWrite5m: 12.50, CacheWrite1h: 20, CacheRead: 0.25, Output: 50},
	{Model: "claude-fable-5", Name: "Claude Fable 5", Input: 10, CacheWrite5m: 12.50, CacheWrite1h: 20, CacheRead: 1, Output: 50},
	{Model: "claude-mythos-5", Name: "Claude Mythos 5", Input: 10, CacheWrite5m: 12.50, CacheWrite1h: 20, CacheRead: 1, Output: 50},
	{Model: "claude-opus-5-5", Name: "Claude Opus 5.5", Input: 4, CacheWrite5m: 5, CacheWrite1h: 8, CacheRead: 0.20, Output: 20, FastMode: true},
	{Model: "claude-opus-5", Name: "Claude Opus 5", Input: 5, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.50, Output: 25, FastMode: true},
	{Model: "claude-opus-4-8", Name: "Claude Opus 4.8", Input: 5, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.50, Output: 25, FastMode: true},
	{Model: "claude-opus-4-7", Name: "Claude Opus 4.7", Input: 5, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.50, Output: 25},
	{Model: "claude-opus-4-6", Name: "Claude Opus 4.6", Input: 5, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.50, Output: 25},
	{Model: "claude-opus-4-5", Name: "Claude Opus 4.5", Input: 5, CacheWrite5m: 6.25, CacheWrite1h: 10, CacheRead: 0.50, Output: 25},
	{Model: "claude-opus-4-1", Name: "Claude Opus 4.1", Input: 15, CacheWrite5m: 18.75, CacheWrite1h: 30, CacheRead: 1.50, Output: 75},
	{Model: "claude-opus-4", Name: "Claude Opus 4", Input: 15, CacheWrite5m: 18.75, CacheWrite1h: 30, CacheRead: 1.50, Output: 75},
	{Model: "claude-sonnet-5-5", Name: "Claude Sonnet 5.5", Input: 2, CacheWrite5m: 2.50, CacheWrite1h: 4, CacheRead: 0.10, Output: 10},
	{Model: "claude-sonnet-5", Name: "Claude Sonnet 5", Input: 2, CacheWrite5m: 2.50, CacheWrite1h: 4, CacheRead: 0.20, Output: 10},
	{Model: "claude-sonnet-4-6", Name: "Claude Sonnet 4.6", Input: 3, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.30, Output: 15},
	{Model: "claude-sonnet-4-5", Name: "Claude Sonnet 4.5", Input: 3, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.30, Output: 15},
	{Model: "claude-sonnet-4", Name: "Claude Sonnet 4", Input: 3, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.30, Output: 15},
	{Model: "claude-haiku-5-5", Name: "Claude Haiku 5.5", Input: 0.10, CacheWrite5m: 0.125, CacheWrite1h: 0.20, CacheRead: 0.01, Output: 0.50,
		LongPromptThreshold: 100_000,
		LongPrompt:          &Price{Input: 0.50, CacheWrite5m: 0.625, CacheWrite1h: 1, CacheRead: 0.05, Output: 2.50}},
	{Model: "claude-haiku-4-5", Name: "Claude Haiku 4.5", Input: 1, CacheWrite5m: 1.25, CacheWrite1h: 2, CacheRead: 0.10, Output: 5},
	{Model: "claude-3-5-haiku", Name: "Claude Haiku 3.5", Input: 0.80, CacheWrite5m: 1, CacheWrite1h: 1.60, CacheRead: 0.08, Output: 4},
	{Model: "claude-haiku-3-5", Name: "Claude Haiku 3.5", Input: 0.80, CacheWrite5m: 1, CacheWrite1h: 1.60, CacheRead: 0.08, Output: 4},
	{Model: "claude-3-7-sonnet", Name: "Claude Sonnet 3.7", Input: 3, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.30, Output: 15, Legacy: true},
	{Model: "claude-3-5-sonnet", Name: "Claude Sonnet 3.5", Input: 3, CacheWrite5m: 3.75, CacheWrite1h: 6, CacheRead: 0.30, Output: 15, Legacy: true},
	{Model: "claude-3-opus", Name: "Claude Opus 3", Input: 15, CacheWrite5m: 18.75, CacheWrite1h: 30, CacheRead: 1.50, Output: 75, Legacy: true},
	{Model: "claude-3-haiku", Name: "Claude Haiku 3", Input: 0.25, CacheWrite5m: 0.30, CacheWrite1h: 0.50, CacheRead: 0.03, Output: 1.25, Legacy: true},
}

// LookupPrice finds the price for a model ID, tolerating routing prefixes
// ("team/claude-..."), cloud prefixes ("us.anthropic.claude-..."), date
// suffixes ("-20251101") and thinking suffixes ("(high)", "-thinking").
func LookupPrice(model string) (Price, bool) {
	name := strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	if idx := strings.Index(name, "("); idx >= 0 {
		name = name[:idx]
	}
	idx := strings.Index(name, "claude-")
	if idx < 0 {
		return Price{}, false
	}
	name = name[idx:]
	best := -1
	for i, p := range Prices {
		if !strings.HasPrefix(name, p.Model) {
			continue
		}
		if rest := name[len(p.Model):]; rest != "" && rest[0] != '-' && rest[0] != '@' && rest[0] != ':' {
			continue
		}
		if best < 0 || len(p.Model) > len(Prices[best].Model) {
			best = i
		}
	}
	if best < 0 {
		return Price{}, false
	}
	return Prices[best], true
}

// tokens is a non-overlapping token split used for pricing.
type tokens struct {
	Input, CacheRead, CacheWrite, Output, Reasoning, Total int64
}

func splitTokens(detail coreusage.Detail) tokens {
	if b := detail.TokenBreakdown; b.Valid() && b.Quality == coreusage.TokenAccountingQualityComplete {
		return tokens{
			Input:      b.Input.UncachedTokens,
			CacheRead:  b.Input.CacheReadTokens,
			CacheWrite: b.Input.CacheWriteTokens,
			Output:     b.Output.TotalTokens,
			Reasoning:  b.Output.ReasoningTokens,
			Total:      b.TotalTokens,
		}
	}
	t := tokens{
		Input:      max(detail.InputTokens, 0),
		CacheRead:  max(detail.CacheReadTokens, 0),
		CacheWrite: max(detail.CacheCreationTokens, 0),
		Output:     max(detail.OutputTokens, 0),
		Reasoning:  max(detail.ReasoningTokens, 0),
		Total:      max(detail.TotalTokens, 0),
	}
	if t.Total == 0 {
		t.Total = t.Input + t.CacheRead + t.CacheWrite + t.Output
	}
	return t
}

// Weight measures how much of a subscription a request consumed: its Anthropic
// API list-price cost when the model is priced, otherwise its tokens in
// millions. Requests through one account normally share a unit.
func Weight(record coreusage.Record) float64 {
	t := splitTokens(record.Detail)
	price, ok := LookupPrice(record.Model)
	if !ok {
		price, ok = LookupPrice(record.ResponseModel)
	}
	if !ok {
		return float64(t.Total) / 1_000_000
	}
	return cost(price, t, strings.EqualFold(strings.TrimSpace(record.Detail.ResponseServiceTier), "fast"))
}

// cost prices one request. Cache writes are priced at the 5-minute rate because
// usage records do not report the cache TTL.
func cost(p Price, t tokens, fast bool) float64 {
	rates := p
	if p.LongPrompt != nil && t.Input+t.CacheRead+t.CacheWrite > p.LongPromptThreshold {
		rates = *p.LongPrompt
	}
	usd := (float64(t.Input)*rates.Input +
		float64(t.CacheRead)*rates.CacheRead +
		float64(t.CacheWrite)*rates.CacheWrite5m +
		float64(t.Output)*rates.Output) / 1_000_000
	if fast && p.FastMode {
		usd *= 2
	}
	return usd
}
