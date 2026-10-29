package config

import "testing"

func TestParseAPIKeyLimitsV8Layout(t *testing.T) {
	raw := `
config-version: 8
access:
  api-keys:
    - "plain-key"
  api-key-limits:
    state-file: "/tmp/usage.json"
    subscriptions:
      - name: pro
        token-limit: 5000000
        period: monthly
    keys:
      - key: "full-key"
        name: admin
      - key: "small-key"
        token-limit: 100000
        period: daily
      - key: "pro-key"
        subscription: pro
`
	cfg, errParse := ParseConfigBytes([]byte(raw))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	limits := cfg.APIKeyLimits
	if limits.StateFile != "/tmp/usage.json" {
		t.Fatalf("state-file = %q", limits.StateFile)
	}
	if len(limits.Subscriptions) != 1 || limits.Subscriptions[0].Name != "pro" || limits.Subscriptions[0].TokenLimit != 5000000 || limits.Subscriptions[0].Period != "monthly" {
		t.Fatalf("subscriptions = %+v", limits.Subscriptions)
	}
	if len(limits.Keys) != 3 || limits.Keys[0].Name != "admin" || limits.Keys[1].TokenLimit != 100000 || limits.Keys[2].Subscription != "pro" {
		t.Fatalf("keys = %+v", limits.Keys)
	}
	got := limits.LimitedAPIKeys()
	if len(got) != 3 || got[0] != "full-key" || got[2] != "pro-key" {
		t.Fatalf("LimitedAPIKeys() = %v", got)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "plain-key" {
		t.Fatalf("api-keys = %v", cfg.APIKeys)
	}
}
