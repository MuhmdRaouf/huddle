package ledger

import (
	"strings"
	"testing"
)

// redactBody's cases: every known secret shape becomes its [redacted:<name>] label, and
// every byte outside a match stands as it was. The values are fakes built in the test;
// never a real key, token or code.

// TestRedactBodyRedactsKnownShapes, one case per rule entry: a planted fake inside a
// message string comes back as exactly its label, count 1.
func TestRedactBodyRedactsKnownShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		fake string
		want string
	}{
		{
			name: "a bearer header keeps its word and loses its token",
			fake: "Bearer " + strings.Repeat("t", 20),
			want: "Bearer [redacted:bearer]",
		},
		{
			name: "an Anthropic-style key",
			fake: "sk-ant-" + strings.Repeat("x", 20),
			want: "[redacted:api-key]",
		},
		{
			name: "a Z.ai-style key",
			fake: strings.Repeat("ab", 16) + "." + strings.Repeat("z", 16),
			want: "[redacted:api-key]",
		},
		{
			name: "a huddle credential",
			fake: "hcred_" + strings.Repeat("x", 20),
			want: "[redacted:huddle-credential]",
		},
		{
			name: "an invite's join token",
			fake: "k3m2x9." + strings.Repeat("a", 16),
			want: "[redacted:join-token]",
		},
		{
			name: "a sign-in code keeps its query name",
			fake: "?code=" + strings.Repeat("q", 24),
			want: "?code=[redacted:sign-in-code]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := []byte(`{"messages":[{"role":"user","content":"use ` + tc.fake + ` now"}]}`)
			got, count := redactBody(body)
			want := `{"messages":[{"role":"user","content":"use ` + tc.want + ` now"}]}`
			if string(got) != want {
				t.Fatalf("redacted = %s, want %s", got, want)
			}
			if count != 1 {
				t.Fatalf("count = %d, want 1", count)
			}
		})
	}
}

// TestRedactBodyLeavesPlainBodiesAlone: nothing that is not a known secret shape moves,
// and the count stays 0.
func TestRedactBodyLeavesPlainBodiesAlone(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
	}{
		{"a token budget", `{"model":"m","max_tokens":1024}`},
		{
			name: "a 40-character git sha",
			body: `{"messages":[{"role":"user","content":"built 0123456789abcdef0123456789abcdef01234567 today"}]}`,
		},
		{
			name: "a uuid",
			body: `{"messages":[{"role":"user","content":"id 123e4567-e89b-12d3-a456-426614174000 seen"}]}`,
		},
		{
			name: "a key stub too short to be a key",
			body: `{"messages":[{"role":"user","content":"the key sk-12 is not one"}]}`,
		},
		{
			name: "a 24-character word with no code= before it",
			body: `{"messages":[{"role":"user","content":"qqqqqqqqqqqqqqqqqqqqqqqq stands alone"}]}`,
		},
		{
			name: "the word Bearer alone",
			body: `{"messages":[{"role":"user","content":"Bearer"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, count := redactBody([]byte(tc.body))
			if string(got) != tc.body {
				t.Fatalf("redacted = %s, want the body unchanged", got)
			}
			if count != 0 {
				t.Fatalf("count = %d, want 0", count)
			}
		})
	}
}
