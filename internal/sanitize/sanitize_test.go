package sanitize

import "testing"

func TestTextFailsClosedForProviderDetails(t *testing.T) {
	for _, input := range []string{
		"upstream failure",
		`{"authorization":"Bearer opaque-value"}`,
		`upstream={\"api-key\":\"opaque-value\"}`,
	} {
		if got := Text(input); got != redactedValue {
			t.Fatalf("Text(%q) = %q, want %q", input, got, redactedValue)
		}
	}
}

func TestTextPreservesEmptyValues(t *testing.T) {
	for _, input := range []string{"", "   "} {
		if got := Text(input); got != input {
			t.Fatalf("Text(%q) = %q", input, got)
		}
	}
}
