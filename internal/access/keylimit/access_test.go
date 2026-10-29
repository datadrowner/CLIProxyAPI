package keylimit

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestCheckRequestAllowsEverythingWithoutLists(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "open"}}}, time.Now())
	if limitErr := l.CheckRequest("open", "claude-opus-5-5", "max"); limitErr != nil {
		t.Fatalf("CheckRequest = %v, want allowed", limitErr)
	}
	if limitErr := l.CheckRequest("not-configured", "anything", "max"); limitErr != nil {
		t.Fatalf("CheckRequest for unconfigured key = %v, want allowed", limitErr)
	}
	if filter := l.ModelFilter("open"); filter != nil {
		t.Fatal("ModelFilter returned a filter for a key without allowed-models")
	}
}

func TestCheckRequestRestrictsModelsAndEfforts(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{
		Key:            "restricted",
		AllowedModels:  []string{"claude-sonnet-*", " GPT-5.5 "},
		AllowedEfforts: []string{"Low", "medium"},
	}}}, time.Now())

	cases := []struct {
		model, effort, code string
	}{
		{"claude-sonnet-5-5", "low", ""},
		{"Claude-Sonnet-5-5", "", ""},
		{"gpt-5.5", "medium", ""},
		{"models/gpt-5.5", "", ""},
		{"claude-opus-5-5", "low", "api_key_model_not_allowed"},
		{"gpt-5.5-mini", "", "api_key_model_not_allowed"},
		{"claude-sonnet-5-5", "high", "api_key_effort_not_allowed"},
		{"claude-sonnet-5-5", "none", "api_key_effort_not_allowed"},
	}
	for _, tc := range cases {
		limitErr := l.CheckRequest("restricted", tc.model, tc.effort)
		got := ""
		if limitErr != nil {
			got = limitErr.Code
		}
		if got != tc.code {
			t.Errorf("CheckRequest(%q, %q) code = %q, want %q", tc.model, tc.effort, got, tc.code)
		}
	}

	filter := l.ModelFilter("restricted")
	if filter == nil || !filter("claude-sonnet-5-5") || filter("claude-haiku-5-5") {
		t.Fatal("ModelFilter does not match the allowed models")
	}
	status, _, _ := l.Check("restricted")
	if len(status.AllowedModels) != 2 || status.AllowedModels[1] != "gpt-5.5" || len(status.AllowedEfforts) != 2 {
		t.Fatalf("status lists = %v / %v, want normalized lists", status.AllowedModels, status.AllowedEfforts)
	}
}

func TestKeyListsOverrideSubscriptionLists(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{
		Subscriptions: []config.APIKeySubscription{{Name: "basic", AllowedModels: []string{"claude-haiku-*"}, AllowedEfforts: []string{"low"}}},
		Keys: []config.APIKeyLimit{
			{Key: "inherits", Subscription: "basic"},
			{Key: "overrides", Subscription: "basic", AllowedModels: []string{"claude-*"}},
		},
	}, time.Now())

	if limitErr := l.CheckRequest("inherits", "claude-sonnet-5-5", ""); limitErr == nil {
		t.Fatal("inherited plan models did not restrict the key")
	}
	if limitErr := l.CheckRequest("overrides", "claude-sonnet-5-5", "low"); limitErr != nil {
		t.Fatalf("key model override not applied: %v", limitErr)
	}
	if limitErr := l.CheckRequest("overrides", "claude-sonnet-5-5", "high"); limitErr == nil {
		t.Fatal("plan efforts should still apply when the key only overrides models")
	}
}

func TestUnknownAllowedEffortBlocksKey(t *testing.T) {
	l, _ := newTestLimiter(t, config.APIKeyLimitsConfig{Keys: []config.APIKeyLimit{{Key: "typo", AllowedEfforts: []string{"hgih"}}}}, time.Now())
	if _, _, limitErr := l.Check("typo"); limitErr == nil || limitErr.Code != "api_key_limit_misconfigured" {
		t.Fatalf("Check = %v, want misconfigured", limitErr)
	}
}

func TestMatchPattern(t *testing.T) {
	cases := []struct {
		pattern, value string
		want           bool
	}{
		{"*", "anything", true},
		{"a*a", "a", false},
		{"a*a", "aa", true},
		{"claude-*-5-5", "claude-opus-5-5", true},
		{"claude-*-5-5", "claude-opus-5-1", false},
		{"*mini*", "gpt-5.5-mini-high", true},
		{"exact", "exactly", false},
	}
	for _, tc := range cases {
		if got := matchPattern(tc.pattern, tc.value); got != tc.want {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tc.pattern, tc.value, got, tc.want)
		}
	}
}
