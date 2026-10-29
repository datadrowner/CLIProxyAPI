package keylimit

import (
	"fmt"
	"strings"
)

// knownEfforts lists the canonical reasoning effort labels a key may be restricted to.
var knownEfforts = map[string]bool{
	"none": true, "auto": true, "minimal": true, "low": true,
	"medium": true, "high": true, "xhigh": true, "max": true,
}

// CheckRequest reports whether the key may call model with the given reasoning
// effort. model must be the requested model without a thinking suffix; an empty
// effort means the request does not set one and is always accepted. Keys that
// are not configured under api-key-limits are always allowed.
func (l *Limiter) CheckRequest(apiKey, model, effort string) *LimitError {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	p, ok := l.policies[strings.TrimSpace(apiKey)]
	l.mu.Unlock()
	if !ok {
		return nil
	}
	model = normalizeModel(model)
	if !modelAllowed(p.allowedModels, model) {
		return &LimitError{
			Message: fmt.Sprintf("model %q is not available for this API key", model),
			Code:    "api_key_model_not_allowed",
		}
	}
	effort = strings.ToLower(strings.TrimSpace(effort))
	if effort != "" && len(p.allowedEfforts) > 0 && !contains(p.allowedEfforts, effort) {
		return &LimitError{
			Message: fmt.Sprintf("reasoning effort %q is not available for this API key (allowed: %s)", effort, strings.Join(p.allowedEfforts, ", ")),
			Code:    "api_key_effort_not_allowed",
		}
	}
	return nil
}

// ModelFilter returns a predicate reporting whether the key may use a model,
// or nil when the key is not restricted to specific models.
func (l *Limiter) ModelFilter(apiKey string) func(model string) bool {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	p, ok := l.policies[strings.TrimSpace(apiKey)]
	l.mu.Unlock()
	if !ok || len(p.allowedModels) == 0 {
		return nil
	}
	return func(model string) bool { return modelAllowed(p.allowedModels, normalizeModel(model)) }
}

func normalizeModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.TrimPrefix(model, "models/")
}

func modelAllowed(patterns []string, model string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if matchPattern(pattern, model) {
			return true
		}
	}
	return false
}

// matchPattern matches value against a pattern where "*" matches any run of characters.
func matchPattern(pattern, value string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == value
	}
	if !strings.HasPrefix(value, parts[0]) {
		return false
	}
	value = value[len(parts[0]):]
	last := parts[len(parts)-1]
	if !strings.HasSuffix(value, last) {
		return false
	}
	value = value[:len(value)-len(last)]
	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(value, part)
		if idx < 0 {
			return false
		}
		value = value[idx+len(part):]
	}
	return true
}

// normalizeList trims, lower-cases and de-duplicates entries, dropping empty ones.
func normalizeList(values []string) []string {
	var out []string
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" && !contains(out, value) {
			out = append(out, value)
		}
	}
	return out
}

func invalidEffort(efforts []string) string {
	for _, effort := range efforts {
		if !knownEfforts[effort] {
			return effort
		}
	}
	return ""
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
