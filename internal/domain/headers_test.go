package domain

import "testing"

// TestCapturedHeader: the allow-list a response header passes before either store keeps it —
// the request id, retry-after, the content type and the rate-limit counters, and nothing
// else, so no credential, cookie or token is ever stored.
func TestCapturedHeader(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"request-id", true},
		{"Request-Id", true},
		{"x-request-id", true},
		{"retry-after", true},
		{"content-type", true},
		{"anthropic-ratelimit-requests-remaining", true},
		{"x-ratelimit-remaining", true},
		{"authorization", false},
		{"Authorization", false},
		{"x-api-key", false},
		{"cookie", false},
		{"set-cookie", false},
		{"proxy-authorization", false},
		{"anthropic-api-key", false},
		{"x-goog-api-key", false},
		{"api-key", false},
		{"x-auth-token", false},
		{"request-id-extra", false},
		{"x-ratelimit-", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := CapturedHeader(tc.name); got != tc.want {
			t.Errorf("CapturedHeader(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}
