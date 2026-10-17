package home

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyFilesHoldOneSecretPerAccount(t *testing.T) {
	dir := t.TempDir()
	secrets := KeyFiles{Dir: dir}
	if err := secrets.Set(bg(), "dns:cloudflare", "tok-1234567890"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	file := filepath.Join(dir, "dns-cloudflare")
	info, err := os.Stat(file)
	if err != nil {
		t.Fatalf("the account's file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %o, want 0600", info.Mode().Perm())
	}
	got, ok := secrets.Get(bg(), "dns:cloudflare")
	if !ok || got != "tok-1234567890" {
		t.Errorf("Get = %q, %v; want the stored token", got, ok)
	}
	// a second account keeps its own file; one account's write never touches another's
	if err := secrets.Set(bg(), "dns:desec", "another"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, ok := secrets.Get(bg(), "dns:desec"); !ok || got != "another" {
		t.Errorf("Get = %q, %v; want another", got, ok)
	}
	if got, ok := secrets.Get(bg(), "dns:cloudflare"); !ok || got != "tok-1234567890" {
		t.Errorf("Get = %q, %v; want the first token kept", got, ok)
	}
	// a stored token is replaced, not appended
	if err := secrets.Set(bg(), "dns:cloudflare", "rotated"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got, ok := secrets.Get(bg(), "dns:cloudflare"); !ok || got != "rotated" {
		t.Errorf("Get = %q, %v; want rotated", got, ok)
	}
	// an account no file holds answers false, never an error
	if _, ok := secrets.Get(bg(), "dns:nobody"); ok {
		t.Error("Get of a missing account answers ok")
	}
}

func TestKeyFilesRefuseAnAccountThatIsNotAName(t *testing.T) {
	secrets := KeyFiles{Dir: t.TempDir()}
	for _, account := range []string{"../escape", "a/b", "", "with space"} {
		if err := secrets.Set(bg(), account, "x"); err == nil {
			t.Errorf("Set(%q) answers no error", account)
		}
	}
}

// bg is one background context for the store's calls.
func bg() context.Context { return context.Background() }
