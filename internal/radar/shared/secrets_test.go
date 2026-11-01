package shared

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/testkit/fakesecret"
)

// RedactSecrets's cases: every known secret shape becomes its [redacted:<name>] label, and
// every byte outside a match stands as it was. The values are fakes built in the test;
// never a real key, token or code.

// TestRedactSecretsRedactsKnownShapes, one case per rule entry: a planted fake inside a
// message string comes back as exactly its label, count 1.
func TestRedactSecretsRedactsKnownShapes(t *testing.T) {
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
			got, count := RedactSecrets(body)
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

// TestRedactSecretsLeavesPlainBodiesAlone: nothing that is not a known secret shape moves,
// and the count stays 0.
func TestRedactSecretsLeavesPlainBodiesAlone(t *testing.T) {
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
			got, count := RedactSecrets([]byte(tc.body))
			if string(got) != tc.body {
				t.Fatalf("redacted = %s, want the body unchanged", got)
			}
			if count != 0 {
				t.Fatalf("count = %d, want 0", count)
			}
		})
	}
}

// TestRedactSecretsRedactsEveryFake: each made-up secret of the shape list, alone, in plain
// text and inside a JSON string after an escaped newline, comes back as exactly its label
// with the count 1, and the JSON around it stays valid.
func TestRedactSecretsRedactsEveryFake(t *testing.T) {
	t.Parallel()
	for _, fake := range fakesecret.All() {
		t.Run(fake.Shape+" in plain text", func(t *testing.T) {
			t.Parallel()
			got, count := RedactSecrets([]byte("line one\n" + fake.Text + "\nline three"))
			want := "line one\n" + fake.Want + "\nline three"
			if string(got) != want {
				t.Fatalf("redacted = %q, want %q", got, want)
			}
			if count != 1 {
				t.Fatalf("count = %d, want 1", count)
			}
		})
		t.Run(fake.Shape+" in JSON text", func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(map[string]string{"text": "line one\n" + fake.Text + "\nline three"})
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, count := RedactSecrets(encoded)
			var decoded map[string]string
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatalf("the redacted JSON does not parse: %v: %s", err, got)
			}
			if want := "line one\n" + fake.Want + "\nline three"; decoded["text"] != want {
				t.Fatalf("redacted text = %q, want %q", decoded["text"], want)
			}
			if count != 1 {
				t.Fatalf("count = %d, want 1", count)
			}
		})
	}
}

// TestRedactSecretsCountsEveryMatch: two secrets of one shape, and secrets of different
// shapes, each count once.
func TestRedactSecretsCountsEveryMatch(t *testing.T) {
	t.Parallel()
	var text []string
	for _, fake := range fakesecret.All() {
		text = append(text, fake.Text)
	}
	text = append(text, text[0])
	_, count := RedactSecrets([]byte(strings.Join(text, "\nand\n")))
	if want := len(text); count != want {
		t.Fatalf("count = %d, want %d", count, want)
	}
}

// TestRedactSecretsLeavesOrdinaryTextAlone: each new shape has a guard that keeps plain
// words, short ids and look-alikes out of its reach.
func TestRedactSecretsLeavesOrdinaryTextAlone(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, text string }{
		{"the word token", "the token is stored in the session and a token budget applies"},
		{"a token count setting", "max_tokens=1024 and MAX_TOKENS=1024"},
		{"a short hex id", "commit a1b2c3d4 and id 9f86d081"},
		{"a 40-character git sha", "built 0123456789abcdef0123456789abcdef01234567 today"},
		{"a word that ends in key", "monkey=banana HOTKEY=ctrl KEYBOARD=us"},
		{"a counter whose name has a key word", "KEY_COUNT=5 TOKEN_LIMIT=10 PASSWORD_MIN=8"},
		{"an empty env value", "API_KEY= and API_KEY=\"\" and API_TOKEN=''"},
		{"an env placeholder", "API_KEY=$API_KEY SECRET_KEY=${SECRET} TOKEN=<your token> PASSWORD=(none) KEY=%KEY%"},
		{"an env value that is already a label", "API_KEY=[redacted:api-key]"},
		{"a lower-case setting", "api_key=none password=none token=none"},
		{"a public key block", "-----BEGIN PUBLIC KEY-----\nabcdef\n-----END PUBLIC KEY-----"},
		{"a certificate block", "-----BEGIN CERTIFICATE-----\nabcdef\n-----END CERTIFICATE-----"},
		{"a private key header with no body", "the " + fakesecret.PrivateKeyHeader() + " header opens it"},
		{"a github prefix with no token", "ghp_short gho_ ghs_abc and github_pat_ alone"},
		{"a slack prefix with no token", "xoxb- xoxz-1234567890123 xox"},
		{"a bare jwt header", "eyJhbGciOiJIUzI1NiJ9 alone and eyJ.eyJ.x"},
		{"a two-segment jwt look-alike", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0"},
		{"a short aws look-alike", "AKIA1234 and ASIA and AKIAZZZZ0000111122223333 is 24 long"},
		{"an aws id with no secret next to it", "id AKIAZZZZ0000 then words only"},
		{"an aws setting with a short value", "aws_secret_access_key = changeme"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, count := RedactSecrets([]byte(tc.text))
			if string(got) != tc.text {
				t.Fatalf("redacted = %q, want the text unchanged", got)
			}
			if count != 0 {
				t.Fatalf("count = %d, want 0", count)
			}
		})
	}
}
