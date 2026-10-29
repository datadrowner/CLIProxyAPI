package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/access/keylimit"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// checkAPIKeyModelAccess rejects client requests whose API key is not allowed
// to use the requested model or reasoning effort (access.api-key-limits).
// Internal requests are not restricted.
func checkAPIKeyModelAccess(ctx context.Context, entryProtocol, modelName string, rawJSON []byte, execOptions modelExecutionOptions) *interfaces.ErrorMessage {
	if execOptions.InternalSource {
		return nil
	}
	apiKey := requestAPIKey(ctx)
	if apiKey == "" {
		return nil
	}
	baseModel := thinking.ParseSuffix(modelName).ModelName
	effort := thinking.ExtractReasoningEffort(rawJSON, entryProtocol, modelName)
	limitErr := keylimit.Default().CheckRequest(apiKey, baseModel, effort)
	if limitErr == nil {
		return nil
	}
	body, errMarshal := json.Marshal(ErrorResponse{Error: ErrorDetail{
		Message: limitErr.Message,
		Type:    "permission_error",
		Code:    limitErr.Code,
	}})
	if errMarshal != nil {
		return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New(limitErr.Message)}
	}
	return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New(string(body))}
}

// requestAPIKey returns the authenticated client API key of the request in ctx.
func requestAPIKey(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	return ginAPIKey(ginCtx)
}

func ginAPIKey(c *gin.Context) string {
	value, exists := c.Get("userApiKey")
	if !exists || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// filterModelList removes models the calling API key may not use from a model
// list body. Entries in the top-level "data" or "models" arrays are matched by
// their "id", "name" or "slug"; entries without one are kept.
func filterModelList(c *gin.Context, body []byte) []byte {
	if c == nil {
		return body
	}
	allowed := keylimit.Default().ModelFilter(ginAPIKey(c))
	if allowed == nil {
		return body
	}
	var doc map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(body, &doc); errUnmarshal != nil {
		return body
	}
	changed := false
	for _, field := range []string{"data", "models"} {
		raw, ok := doc[field]
		if !ok {
			continue
		}
		var entries []json.RawMessage
		if errUnmarshal := json.Unmarshal(raw, &entries); errUnmarshal != nil {
			continue
		}
		kept := make([]json.RawMessage, 0, len(entries))
		for _, entry := range entries {
			var ids struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			}
			_ = json.Unmarshal(entry, &ids)
			model := ids.ID
			if model == "" {
				model = ids.Name
			}
			if model == "" {
				model = ids.Slug
			}
			if model == "" || allowed(model) {
				kept = append(kept, entry)
			}
		}
		if len(kept) == len(entries) {
			continue
		}
		encoded, errMarshal := json.Marshal(kept)
		if errMarshal != nil {
			continue
		}
		doc[field] = encoded
		changed = true
	}
	if !changed {
		return body
	}
	filtered, errMarshal := json.Marshal(doc)
	if errMarshal != nil {
		return body
	}
	return filtered
}
