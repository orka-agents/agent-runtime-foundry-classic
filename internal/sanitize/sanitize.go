package sanitize

import "strings"

const redactedValue = "[REDACTED]"

// Text intentionally fails closed for provider-originated error details. The
// adapter retains structured reason and HTTP status fields elsewhere; opaque
// upstream text is not required for control flow and may contain credentials.
func Text(value string) string {
	if strings.TrimSpace(value) == "" {
		return value
	}
	return redactedValue
}
