package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestV8ConfigPutAPIKeyLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\naccess:\n  api-keys:\n    - main-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.Handle(http.MethodGet, "/v8/management/config/*path", h.ConfigV8)
	router.Handle(http.MethodPut, "/v8/management/config/*path", h.ConfigV8)

	body := `{"subscriptions":[{"name":"pro","token-limit":1000,"period":"daily"}],"keys":[{"key":"sk-a","name":"alice","subscription":"pro"},{"key":"sk-b","token-limit":50}]}`
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config/access/api-key-limits", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(h.cfg.APIKeyLimits.Keys) != 2 || h.cfg.APIKeyLimits.Keys[0].Subscription != "pro" || h.cfg.APIKeyLimits.Subscriptions[0].TokenLimit != 1000 {
		t.Fatalf("runtime config = %+v", h.cfg.APIKeyLimits)
	}
	if len(h.cfg.APIKeys) != 1 || h.cfg.APIKeys[0] != "main-key" {
		t.Fatalf("api-keys changed: %v", h.cfg.APIKeys)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v8/management/config/access/api-key-limits", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var got config.APIKeyLimitsConfig
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &got); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(got.Keys) != 2 || got.Keys[1].Key != "sk-b" || got.Keys[1].TokenLimit != 50 {
		t.Fatalf("GET = %+v", got)
	}
}
