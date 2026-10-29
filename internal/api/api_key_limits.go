package api

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/access/keylimit"
)

// apiKeyUsagePath lets a client inspect its own budget; it stays reachable
// after the budget is exhausted.
const apiKeyUsagePath = "/v1/api-key/usage"

// enforceAPIKeyLimit sets token budget headers for keys configured under
// access.api-key-limits and aborts the request when the budget is exhausted.
// It returns false when the request was aborted.
func enforceAPIKeyLimit(c *gin.Context, apiKey string) bool {
	status, tracked, limitErr := keylimit.Default().Check(apiKey)
	if !tracked {
		return true
	}
	if !status.Unlimited {
		c.Header("X-RateLimit-Limit-Tokens", strconv.FormatInt(status.TokenLimit, 10))
		c.Header("X-RateLimit-Remaining-Tokens", strconv.FormatInt(status.RemainingTokens, 10))
		if !status.ResetsAt.IsZero() {
			c.Header("X-RateLimit-Reset-Tokens", status.ResetsAt.UTC().Format(time.RFC3339))
		}
	}
	if limitErr == nil || c.FullPath() == apiKeyUsagePath {
		return true
	}

	closeUnconsumedRequestOnAbort(c)
	statusCode := http.StatusTooManyRequests
	errorType := "rate_limit_error"
	if limitErr.Code == "api_key_limit_misconfigured" {
		statusCode = http.StatusForbidden
		errorType = "permission_error"
	}
	if limitErr.RetryAfter > 0 {
		c.Header("Retry-After", strconv.FormatInt(int64(math.Ceil(limitErr.RetryAfter.Seconds())), 10))
	}
	c.AbortWithStatusJSON(statusCode, gin.H{"error": gin.H{
		"message": limitErr.Message,
		"type":    errorType,
		"param":   nil,
		"code":    limitErr.Code,
	}})
	return false
}

// apiKeyUsageHandler reports the token budget of the calling API key.
func apiKeyUsageHandler(c *gin.Context) {
	apiKey := ""
	if value, exists := c.Get("userApiKey"); exists {
		if s, ok := value.(string); ok {
			apiKey = strings.TrimSpace(s)
		}
	}
	status, tracked, limitErr := keylimit.Default().Check(apiKey)
	if !tracked {
		c.JSON(http.StatusOK, gin.H{"limited": false, "unlimited": true})
		return
	}
	body := gin.H{"limited": !status.Unlimited, "usage": status, "blocked": limitErr != nil}
	if limitErr != nil {
		body["reason"] = limitErr.Message
	}
	c.JSON(http.StatusOK, body)
}
