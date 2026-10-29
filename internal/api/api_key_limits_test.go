package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/access/keylimit"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestAPIKeyLimitsEnforcedByAuthMiddleware(t *testing.T) {
	cfg := &proxyconfig.Config{}
	cfg.APIKeys = []string{"plain-key"}
	cfg.APIKeyLimits = proxyconfig.APIKeyLimitsConfig{
		StateFile: filepath.Join(t.TempDir(), "usage.json"),
		Subscriptions: []proxyconfig.APIKeySubscription{
			{Name: "starter", TokenLimit: 100, Period: "daily"},
		},
		Keys: []proxyconfig.APIKeyLimit{
			{Key: "full-key"},
			{Key: "starter-key", Subscription: "starter"},
		},
	}
	server := newTestServerWithConfig(t, cfg)
	configaccess.Register(&cfg.SDKConfig)
	server.accessManager.SetProviders(sdkaccess.RegisteredProviders())
	keylimit.Default().Configure(cfg)
	t.Cleanup(func() {
		keylimit.Default().Configure(nil)
		configaccess.Register(nil)
	})

	do := func(method, path, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		return rr
	}

	rr := do(http.MethodGet, apiKeyUsagePath, "starter-key")
	if rr.Code != http.StatusOK || rr.Header().Get("X-RateLimit-Limit-Tokens") != "100" {
		t.Fatalf("usage before spend: code=%d limit header=%q body=%s", rr.Code, rr.Header().Get("X-RateLimit-Limit-Tokens"), rr.Body.String())
	}

	for _, key := range []string{"full-key", "starter-key"} {
		keylimit.Default().HandleUsage(context.Background(), coreusage.Record{APIKey: key, Detail: coreusage.Detail{TotalTokens: 150}})
	}

	rr = do(http.MethodPost, "/v1/chat/completions", "starter-key")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("exhausted key: code=%d body=%s, want 429", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "api_key_token_limit_exceeded") || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("exhausted key: body=%s retry-after=%q", rr.Body.String(), rr.Header().Get("Retry-After"))
	}

	rr = do(http.MethodGet, apiKeyUsagePath, "starter-key")
	var body struct {
		Limited bool            `json:"limited"`
		Blocked bool            `json:"blocked"`
		Usage   keylimit.Status `json:"usage"`
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("usage after spend: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if errUnmarshal := json.Unmarshal(rr.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode usage: %v", errUnmarshal)
	}
	if !body.Limited || !body.Blocked || body.Usage.UsedTokens != 150 || body.Usage.Subscription != "starter" {
		t.Fatalf("usage body = %+v", body)
	}

	for _, key := range []string{"full-key", "plain-key"} {
		if rr = do(http.MethodPost, "/v1/chat/completions", key); rr.Code == http.StatusTooManyRequests || rr.Code == http.StatusUnauthorized {
			t.Fatalf("%s: code=%d body=%s, want request to pass auth and limits", key, rr.Code, rr.Body.String())
		}
	}
}
