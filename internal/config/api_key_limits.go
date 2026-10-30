package config

import "strings"

// APIKeyLimitsConfig configures per-client API keys with individual token budgets.
// Keys listed here are accepted for client authentication in addition to access.api-keys.
type APIKeyLimitsConfig struct {
	// StateFile stores the consumed token counters so budgets survive restarts.
	// Empty uses "api-key-usage.state" inside the auth directory. Do not use a
	// *.json name inside the auth directory: those files are loaded as credentials.
	StateFile string `yaml:"state-file,omitempty" json:"state-file,omitempty"`

	// Subscriptions defines named plans that keys can reference.
	Subscriptions []APIKeySubscription `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`

	// Keys lists client API keys with their limits.
	Keys []APIKeyLimit `yaml:"keys,omitempty" json:"keys,omitempty"`
}

// APIKeySubscription is a named plan that defines a token budget per period.
type APIKeySubscription struct {
	// Name identifies the subscription; keys reference it by this value.
	Name string `yaml:"name" json:"name"`

	// TokenLimit is the token budget per period. Values <= 0 mean unlimited.
	TokenLimit int64 `yaml:"token-limit,omitempty" json:"token-limit,omitempty"`

	// Period controls when the budget resets: "total" (never, default), "daily",
	// "weekly", "monthly" (calendar periods in UTC), or a Go duration such as "5h".
	Period string `yaml:"period,omitempty" json:"period,omitempty"`

	// AccountShare is the percentage (0-100] of each connected subscription
	// account's limit windows (e.g. Claude 5h/7d) a key may consume. It resets
	// when the account's window resets. Values <= 0 disable it.
	AccountShare float64 `yaml:"account-share,omitempty" json:"account-share,omitempty"`

	// AllowedModels restricts which models a key may request and list. Entries
	// match the requested model name case-insensitively and may use "*"
	// wildcards (e.g. "claude-sonnet-*"). Empty allows every model.
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`

	// AllowedEfforts restricts which reasoning efforts a key may request:
	// none, auto, minimal, low, medium, high, xhigh, max. Requests that do not
	// set an effort are accepted and use the provider default. Empty allows every effort.
	AllowedEfforts []string `yaml:"allowed-efforts,omitempty" json:"allowed-efforts,omitempty"`
}

// APIKeyLimit configures one client API key.
//
// A key without token-limit and without subscription has full (unlimited) usage.
// When a subscription is set, its token-limit and period apply unless the key
// overrides them with its own values.
type APIKeyLimit struct {
	// Key is the client API key value.
	Key string `yaml:"key" json:"key"`

	// Name is an optional human-readable label shown in usage reports.
	Name string `yaml:"name,omitempty" json:"name,omitempty"`

	// Subscription references an entry in APIKeyLimitsConfig.Subscriptions.
	Subscription string `yaml:"subscription,omitempty" json:"subscription,omitempty"`

	// TokenLimit is the token budget per period. Values <= 0 inherit from the
	// subscription, or mean unlimited when no subscription is set.
	TokenLimit int64 `yaml:"token-limit,omitempty" json:"token-limit,omitempty"`

	// Period overrides the subscription period. See APIKeySubscription.Period.
	Period string `yaml:"period,omitempty" json:"period,omitempty"`

	// AccountShare overrides the subscription account share. See APIKeySubscription.AccountShare.
	AccountShare float64 `yaml:"account-share,omitempty" json:"account-share,omitempty"`

	// AllowedModels overrides the subscription model list when non-empty. See APIKeySubscription.AllowedModels.
	AllowedModels []string `yaml:"allowed-models,omitempty" json:"allowed-models,omitempty"`

	// AllowedEfforts overrides the subscription effort list when non-empty. See APIKeySubscription.AllowedEfforts.
	AllowedEfforts []string `yaml:"allowed-efforts,omitempty" json:"allowed-efforts,omitempty"`
}

// LimitedAPIKeys returns the trimmed, non-empty keys configured under api-key-limits.
func (c APIKeyLimitsConfig) LimitedAPIKeys() []string {
	if len(c.Keys) == 0 {
		return nil
	}
	keys := make([]string, 0, len(c.Keys))
	for _, entry := range c.Keys {
		if key := strings.TrimSpace(entry.Key); key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}
