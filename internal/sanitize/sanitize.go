package sanitize

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/orka-agents/agent-runtime-foundry-classic/internal/redact"
)

const redactedValue = "[REDACTED]"

var (
	authorizationHeaderRe = regexp.MustCompile(`(?i)\b(authorization\s*:\s*)[^\r\n]+`)
	transactionHeaderRe   = regexp.MustCompile(`(?i)\b((?:txn-token|transaction-token)\s*:\s*)[A-Za-z0-9._~+/=-]+`)
	cookieHeaderRe        = regexp.MustCompile(`(?i)\b((?:cookie|set-cookie)\s*:\s*)[^\r\n]+`)
	structuredHeaderRe    = regexp.MustCompile(
		`(?i)(["']?(?:authorization|cookie|set-cookie|txn-token|transaction-token)["']?\s*:\s*)` +
			`(?:"(?:\\.|[^"])*"|'(?:\\.|[^'])*'|\[[^]\r\n]*\]|[^\s,}\r\n]+)`,
	)
)

// Text redacts credential-shaped content before it can surface in adapter or
// client errors.
func Text(value string) string {
	if structured, ok := redactJSON(value); ok {
		value = structured
	}
	value = redact.SensitiveText(value)
	value = structuredHeaderRe.ReplaceAllString(value, `${1}`+redactedValue)
	value = authorizationHeaderRe.ReplaceAllString(value, `${1}`+redactedValue)
	value = transactionHeaderRe.ReplaceAllString(value, `${1}`+redactedValue)
	return cookieHeaderRe.ReplaceAllString(value, `${1}`+redactedValue)
}

func redactJSON(value string) (string, bool) {
	trimmed := strings.TrimSpace(value)
	if !json.Valid([]byte(trimmed)) {
		return value, false
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return value, false
	}
	encoded, err := json.Marshal(redactJSONValue(decoded))
	if err != nil {
		return value, false
	}
	return string(encoded), true
}

func redactJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, child := range typed {
			if sensitiveJSONKey(key) {
				out[key] = redactedValue
				continue
			}
			out[key] = redactJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = redactJSONValue(child)
		}
		return out
	case string:
		return redact.SensitiveText(typed)
	default:
		return typed
	}
}

func sensitiveJSONKey(key string) bool {
	normalized := strings.ToLower(key)
	normalized = strings.NewReplacer("-", "", "_", "", ".", "", " ", "").Replace(normalized)
	for _, marker := range []string{
		"apikey", "token", "secret", "password", "passwd", "pwd", "credential",
		"privatekey", "clientsecret", "accesstoken", "refreshtoken", "authorization", "cookie",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
