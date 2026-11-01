// Package fakesecret builds made-up secrets of every shape the radar stores redact, for the
// tests that prove none of them reaches a store. No value here is real or comes from any
// store; each is assembled at run time from short parts, so the source file holds no
// complete secret shape for a scanner to flag.
package fakesecret

import "strings"

// Fake is one made-up secret inside the text a transcript or a router body carries.
type Fake struct {
	// Shape names the secret shape, for test names.
	Shape string
	// Text is the fake as it stands in a line of text.
	Text string
	// Secrets are the substrings of Text that must not survive redaction.
	Secrets []string
	// Want is what Text becomes once redacted.
	Want string
}

func rep(part string, n int) string { return strings.Repeat(part, n) }

// All answers one fake per secret shape, one per prefix where a shape has several.
func All() []Fake {
	awsID := "AK" + "IA" + "ZZZZ" + "0000" + "1111" + "2222"
	awsSTSID := "AS" + "IA" + "ZZZZ" + "0000" + "1111" + "2222"
	awsSecret := rep("aB3/", 10)
	pemBody := []string{rep("Zm", 32), rep("Yx", 32), rep("Wv", 12) + "=="}
	pem := "-----BEGIN RSA " + "PRIVATE KEY-----\n" + strings.Join(pemBody, "\n") +
		"\n-----END RSA " + "PRIVATE KEY-----"
	fakes := []Fake{
		{Shape: "bearer", Text: "Bearer " + rep("t", 20), Secrets: []string{rep("t", 20)}, Want: "Bearer [redacted:bearer]"},
		{Shape: "sk key", Text: "sk-ant-" + rep("x", 20), Secrets: []string{rep("x", 20)}, Want: "[redacted:api-key]"},
		{
			Shape: "z.ai key", Text: rep("ab", 16) + "." + rep("z", 16),
			Secrets: []string{rep("ab", 16), rep("z", 16)}, Want: "[redacted:api-key]",
		},
		{
			Shape: "huddle credential", Text: "hcred_" + rep("h", 20),
			Secrets: []string{rep("h", 20)}, Want: "[redacted:huddle-credential]",
		},
		{
			Shape: "join token", Text: "k3m2x9." + rep("d", 16),
			Secrets: []string{"k3m2x9", rep("d", 16)}, Want: "[redacted:join-token]",
		},
		{
			Shape: "sign-in code", Text: "?code=" + rep("q", 24),
			Secrets: []string{rep("q", 24)}, Want: "?code=[redacted:sign-in-code]",
		},
		{Shape: "aws access key", Text: awsID, Secrets: []string{awsID}, Want: "[redacted:aws-key]"},
		{Shape: "aws sts access key", Text: awsSTSID, Secrets: []string{awsSTSID}, Want: "[redacted:aws-key]"},
		{
			Shape: "aws key pair", Text: awsID + " " + awsSecret,
			Secrets: []string{awsID, awsSecret}, Want: "[redacted:aws-key]",
		},
		{
			Shape: "aws secret key setting", Text: "aws_secret_access_key = " + awsSecret,
			Secrets: []string{awsSecret}, Want: "aws_secret_access_key = [redacted:aws-key]",
		},
		{Shape: "slack bot token", Text: "xo" + "xb-" + "1234567890-" + rep("e", 12), Secrets: []string{"1234567890-" + rep("e", 12)}, Want: "[redacted:slack-token]"},
		{Shape: "slack user token", Text: "xo" + "xp-" + "1234567890-" + rep("f", 12), Secrets: []string{"1234567890-" + rep("f", 12)}, Want: "[redacted:slack-token]"},
		{
			Shape: "jwt", Text: "eyJ" + rep("j", 10) + ".eyJ" + rep("w", 10) + "." + rep("m", 12),
			Secrets: []string{rep("j", 10), rep("w", 10), rep("m", 12)}, Want: "[redacted:jwt]",
		},
		{
			Shape: "private key block", Text: pem, Secrets: pemBody, Want: "[redacted:private-key]",
		},
		{
			Shape: "env password", Text: "DB_PASSWORD=" + "hunter2-" + rep("p", 8),
			Secrets: []string{"hunter2-" + rep("p", 8)}, Want: "DB_PASSWORD=[redacted:env-secret]",
		},
		{
			Shape: "env secret key", Text: "STRIPE_SECRET_KEY=" + rep("v", 14),
			Secrets: []string{rep("v", 14)}, Want: "STRIPE_SECRET_KEY=[redacted:env-secret]",
		},
		{
			Shape: "env token", Text: "NPM_TOKEN=" + rep("n", 16),
			Secrets: []string{rep("n", 16)}, Want: "NPM_TOKEN=[redacted:env-secret]",
		},
		{
			Shape: "env credential", Text: "GOOGLE_CREDENTIALS=" + rep("g", 12),
			Secrets: []string{rep("g", 12)}, Want: "GOOGLE_CREDENTIALS=[redacted:env-secret]",
		},
		{
			Shape: "env quoted value", Text: `SESSION_SECRET="two words ` + rep("s", 6) + `"`,
			Secrets: []string{"two words " + rep("s", 6)}, Want: "SESSION_SECRET=[redacted:env-secret]",
		},
	}
	for _, prefix := range []string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_"} {
		token := "gh" + prefix[2:] + rep("a", 36)
		fakes = append(fakes, Fake{
			Shape: "github " + prefix, Text: token, Secrets: []string{rep("a", 36)}, Want: "[redacted:github-token]",
		})
	}
	pat := "github" + "_pat_" + rep("A", 22) + "_" + rep("c", 30)
	return append(fakes, Fake{
		Shape: "github fine-grained token", Text: pat, Secrets: []string{rep("A", 22), rep("c", 30)}, Want: "[redacted:github-token]",
	})
}

// PrivateKeyHeader is the opening marker line of a private key block, with no body. It is
// assembled at run time so no source file holds the marker as one literal.
func PrivateKeyHeader() string { return "-----BEGIN " + "PRIVATE KEY-----" }
