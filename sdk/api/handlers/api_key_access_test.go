package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/access/keylimit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func configureRestrictedKey(t *testing.T) {
	t.Helper()
	cfg := &config.Config{}
	cfg.APIKeyLimits = config.APIKeyLimitsConfig{
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		Keys: []config.APIKeyLimit{{
			Key:            "sk-restricted",
			AllowedModels:  []string{"claude-sonnet-*"},
			AllowedEfforts: []string{"low", "medium"},
		}},
	}
	keylimit.Default().Configure(cfg)
	t.Cleanup(func() { keylimit.Default().Configure(&config.Config{}) })
}

func ginContextWithKey(apiKey string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	c.Set("userApiKey", apiKey)
	return c, recorder
}

func TestExecuteRejectsModelAndEffortNotAllowedForKey(t *testing.T) {
	configureRestrictedKey(t)
	c, _ := ginContextWithKey("sk-restricted")
	ctx := context.WithValue(context.Background(), "gin", c)
	h := &BaseAPIHandler{}

	_, _, errMsg := h.ExecuteWithAuthManager(ctx, "claude", "claude-opus-5-5", []byte(`{"model":"claude-opus-5-5"}`), "")
	if errMsg == nil || errMsg.StatusCode != http.StatusForbidden || !strings.Contains(errMsg.Error.Error(), "api_key_model_not_allowed") {
		t.Fatalf("model error = %+v, want 403 api_key_model_not_allowed", errMsg)
	}

	_, _, errChan := h.ExecuteStreamWithAuthManager(ctx, "claude", "claude-sonnet-5-5", []byte(`{"model":"claude-sonnet-5-5","output_config":{"effort":"max"}}`), "")
	errMsg = <-errChan
	if errMsg == nil || errMsg.StatusCode != http.StatusForbidden || !strings.Contains(errMsg.Error.Error(), "api_key_effort_not_allowed") {
		t.Fatalf("effort error = %+v, want 403 api_key_effort_not_allowed", errMsg)
	}

	if errMsg = checkAPIKeyModelAccess(ctx, "openai", "claude-sonnet-5-5(low)", []byte(`{}`), modelExecutionOptions{}); errMsg != nil {
		t.Fatalf("allowed model with allowed suffix effort rejected: %v", errMsg.Error)
	}
	if errMsg = checkAPIKeyModelAccess(ctx, "claude", "claude-opus-5-5", nil, modelExecutionOptions{InternalSource: true}); errMsg != nil {
		t.Fatalf("internal request rejected: %v", errMsg.Error)
	}
}

func TestWriteModelListResponseFiltersModelsForKey(t *testing.T) {
	configureRestrictedKey(t)
	c, recorder := ginContextWithKey("sk-restricted")
	h := &BaseAPIHandler{}
	h.WriteModelListResponse(c, "openai", gin.H{"object": "list", "data": []gin.H{
		{"id": "claude-sonnet-5-5"}, {"id": "claude-opus-5-5"}, {"id": "gpt-5.5"},
	}})

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode body: %v (%s)", errUnmarshal, recorder.Body.String())
	}
	if len(body.Data) != 1 || body.Data[0].ID != "claude-sonnet-5-5" {
		t.Fatalf("models = %+v, want only claude-sonnet-5-5", body.Data)
	}

	other, otherRecorder := ginContextWithKey("sk-unrestricted")
	h.WriteModelListResponse(other, "gemini", gin.H{"models": []gin.H{{"name": "models/a"}, {"name": "models/b"}}})
	if !strings.Contains(otherRecorder.Body.String(), "models/b") {
		t.Fatalf("unrestricted key list was filtered: %s", otherRecorder.Body.String())
	}
}
