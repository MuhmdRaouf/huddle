package main

// `huddle cert` as the binary refuses it: a missing provider, a provider the issuer
// does not know and a machine with no hub name each answer one usage line and exit 2 — before
// any token is asked for, so nothing secret is ever typed into a refused line. The real issuer
// knows its providers by name, so the unknown name here is answered by the real check.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCertRefusesUsageBeforeAnythingIsTyped(t *testing.T) {
	cases := []struct {
		name string
		line []string
	}{
		{"no provider", []string{"cert"}},
		{"a provider the issuer does not know", []string{"cert", "--provider", "no-such-dns-provider"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := aloneEnv(t)
			code, out, errOut := aloneExec(t, env, c.line...)
			if code != 2 || out != "" || errOut == "" {
				t.Fatalf("huddle %v = (%d, %q, %q), want 2 with one usage line", c.line, code, out, errOut)
			}
			if strings.Contains(out+errOut, "token") {
				t.Errorf("a refused line still asks for the token:\n%s%s", out, errOut)
			}
			if _, err := os.Stat(filepath.Join(env["HUDDLE_HOME"], "tls")); !os.IsNotExist(err) {
				t.Errorf("a refused line wrote a certificate anyway: %v", err)
			}
		})
	}
}

func TestCertNeedsTheHubsNameFirst(t *testing.T) {
	env := aloneEnv(t)
	// the provider is one the issuer knows, so the refusal is the missing name
	code, out, errOut := aloneExec(t, env, "cert", "--provider", "cloudflare")
	if code != 2 || out != "" || errOut == "" {
		t.Fatalf("huddle cert = (%d, %q, %q), want 2 with one usage line", code, out, errOut)
	}
	if !strings.Contains(errOut, "role hub") {
		t.Errorf("the usage line does not point at the role word: %q", errOut)
	}
}
