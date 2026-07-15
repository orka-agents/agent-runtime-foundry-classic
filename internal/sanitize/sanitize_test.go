package sanitize

import (
	"strings"
	"testing"
)

func TestTextRedactsHeadersAndKeys(t *testing.T) {
	input := "Authorization: Bearer secret-value\nTxn-Token: mock-token\napi_key=sk-12345678901234567890"
	got := Text(input)
	for _, secret := range []string{"secret-value", "mock-token", "sk-12345678901234567890"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Text() leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, redactedValue) {
		t.Fatalf("Text() = %q, want redaction marker", got)
	}
}

func TestTextRedactsStructuredHeaderValues(t *testing.T) {
	input := `{"authorization":"Bearer json-secret","cookie":["session=array-secret"],"txn-token":"txn-secret"}`
	got := Text(input)
	for _, secret := range []string{"json-secret", "array-secret", "txn-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("Text() leaked %q in %q", secret, got)
		}
	}
}

func TestTextRedactsMultilineStructuredHeaders(t *testing.T) {
	input := "{\n  \"headers\": {\n    \"cookie\": [\n      \"sessionid=plain-secret\"\n    ]\n  }\n}"
	got := Text(input)
	if strings.Contains(got, "plain-secret") || !strings.Contains(got, redactedValue) {
		t.Fatalf("Text() = %q, want multiline cookie redaction", got)
	}
}
