package main

// `huddle trust` and `huddle untrust` as the binary runs them, on the shapes that never reach a
// real trust store: an empty home answers that nothing is installed, and a broken CA is refused
// before the store is ever asked. The installing shapes are proven against the fake store in
// internal/huddle/cli — no test here may touch the machine's real stores.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUntrustOnAnEmptyHomeAnswersQuietly(t *testing.T) {
	env := aloneEnv(t)
	code, out, errOut := aloneExec(t, env, "untrust")
	if code != 0 || errOut != "" {
		t.Fatalf("huddle untrust = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("an empty home does not say the root is not installed:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(env["HUDDLE_HOME"], "pki")); !os.IsNotExist(err) {
		t.Errorf("untrust created a CA anyway: %v", err)
	}
}

func TestTrustRefusesABrokenCABeforeAnyStoreIsAsked(t *testing.T) {
	env := aloneEnv(t)
	pki := filepath.Join(env["HUDDLE_HOME"], "pki")
	if err := os.MkdirAll(pki, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pki, "root.crt"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pki, "root.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := aloneExec(t, env, "trust")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot serve") {
		t.Fatalf("huddle trust over a broken CA = (%d, %q, %q), want 1 naming the CA", code, out, errOut)
	}
}

func TestTheTrustWordsRefuseWrongShapes(t *testing.T) {
	for _, argv := range [][]string{
		{"trust", "extra"},
		{"untrust", "extra"},
		{"trust", "extra", "--phone"},
		{"trust", "--phone", "--force"},
	} {
		env := aloneEnv(t)
		code, out, errOut := aloneExec(t, env, argv...)
		if code != 2 || out != "" || !strings.Contains(errOut, "usage: huddle trust") {
			t.Fatalf("huddle %v = (%d, %q, %q), want 2 with the usage", argv, code, out, errOut)
		}
	}
}

// The phone shape over a broken CA is refused before anything binds, like the installing one.
func TestTrustPhoneRefusesABrokenCABeforeItBinds(t *testing.T) {
	env := aloneEnv(t)
	pki := filepath.Join(env["HUDDLE_HOME"], "pki")
	if err := os.MkdirAll(pki, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pki, "root.crt"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pki, "root.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := aloneExec(t, env, "trust", "--phone")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot serve") {
		t.Fatalf("huddle trust --phone over a broken CA = (%d, %q, %q), want 1 naming the CA", code, out, errOut)
	}
}
