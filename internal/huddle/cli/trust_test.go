package cli

// `huddle trust` and `huddle untrust` against a fake store and a fake pool: the root is
// installed once with the marker that says huddle did it, both words are idempotent, and a
// broken CA is refused before the store is ever reached. No test here touches a real trust
// store — the fake answers through the world's hooks.

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/pki"
)

// fakeTrustStore counts the installs and removals and can refuse either.
type fakeTrustStore struct {
	installs   int
	uninstalls int
	installErr error
}

func (f *fakeTrustStore) Install(*x509.Certificate) error {
	f.installs++
	return f.installErr
}

func (f *fakeTrustStore) Uninstall(*x509.Certificate) error {
	f.uninstalls++
	return nil
}

// trustWorld is one trust run's world: a temp home, the fake store, and a pool the test fills
// the way an install would fill the machine's.
type trustWorld struct {
	w        *World
	home     string
	store    *fakeTrustStore
	trusted  *x509.Certificate // non-nil: the pool vouches for this root
	poolFunc func() *x509.CertPool
}

func newTrustWorld(t *testing.T) *trustWorld {
	t.Helper()
	homeDir := t.TempDir()
	world := &trustWorld{home: homeDir, store: &fakeTrustStore{}}
	world.poolFunc = func() *x509.CertPool {
		if world.trusted == nil {
			return x509.NewCertPool()
		}
		pool := x509.NewCertPool()
		pool.AddCert(world.trusted)
		return pool
	}
	world.w = &World{
		Getenv: func(key string) string {
			if key == "HUDDLE_HOME" {
				return homeDir
			}
			return ""
		},
		TrustStore: func() pki.Store { return world.store },
		SystemPool: world.poolFunc,
	}
	return world
}

// trustRun runs one of the words and answers its exit code with both streams.
func trustRun(t *testing.T, world *trustWorld, word string, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	run := Trust
	if word == "untrust" {
		run = Untrust
	}
	code := run(context.Background(), world.w, argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

// trustRootOf answers the root the word minted in the world's home.
func trustRootOf(t *testing.T, world *trustWorld) *x509.Certificate {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(world.home, "pki", "root.crt"))
	if err != nil {
		t.Fatalf("read the minted root: %v", err)
	}
	block, _ := pem.Decode(body)
	if block == nil {
		t.Fatal("pki/root.crt holds no certificate")
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestTrustInstallsTheRootAndMarksItAsHuddles(t *testing.T) {
	world := newTrustWorld(t)
	code, out, errOut := trustRun(t, world, "trust", "trust")
	if code != 0 || errOut != "" {
		t.Fatalf("huddle trust = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if world.store.installs != 1 {
		t.Errorf("the store was asked %d times, want once", world.store.installs)
	}
	if !strings.HasPrefix(out, "Installing Huddle's local CA root; your OS may ask for your password.") {
		t.Errorf("the first line does not warn about the password:\n%s", out)
	}
	if !strings.Contains(out, "trusted") {
		t.Errorf("the answer does not say the root is trusted:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(world.home, "pki", "trusted")); err != nil {
		t.Errorf("the trusted marker did not land: %v", err)
	}
}

func TestTrustIsIdempotent(t *testing.T) {
	world := newTrustWorld(t)
	if code, _, _ := trustRun(t, world, "trust", "trust"); code != 0 {
		t.Fatal("the first trust failed")
	}
	world.trusted = trustRootOf(t, world) // the machine's pool now vouches for the root
	code, out, errOut := trustRun(t, world, "trust", "trust")
	if code != 0 || errOut != "" {
		t.Fatalf("the second huddle trust = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "already trusted") {
		t.Errorf("the second run does not say the root is already trusted:\n%s", out)
	}
	if world.store.installs != 1 {
		t.Errorf("the store was asked %d times over two runs, want once", world.store.installs)
	}
}

func TestUntrustRemovesTheRootAndTheMarker(t *testing.T) {
	world := newTrustWorld(t)
	if code, _, _ := trustRun(t, world, "trust", "trust"); code != 0 {
		t.Fatal("the first trust failed")
	}
	world.trusted = trustRootOf(t, world)
	code, out, errOut := trustRun(t, world, "untrust", "untrust")
	if code != 0 || errOut != "" {
		t.Fatalf("huddle untrust = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if world.store.uninstalls != 1 {
		t.Errorf("the store was asked %d times, want once", world.store.uninstalls)
	}
	if !strings.Contains(out, "removed") {
		t.Errorf("the answer does not say the root was removed:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(world.home, "pki", "trusted")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the trusted marker survived: %v", err)
	}
}

func TestUntrustIsIdempotentAndAnswersAnEmptyHome(t *testing.T) {
	world := newTrustWorld(t)
	code, out, errOut := trustRun(t, world, "untrust", "untrust")
	if code != 0 || errOut != "" {
		t.Fatalf("huddle untrust on a home with no CA = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "not installed") {
		t.Errorf("an empty home does not say the root is not installed:\n%s", out)
	}
	if world.store.uninstalls != 0 {
		t.Error("an empty home still reached the store")
	}
	// a trusted root that was removed answers the same way the second time
	if code, _, _ := trustRun(t, world, "trust", "trust"); code != 0 {
		t.Fatal("the trust failed")
	}
	world.trusted = trustRootOf(t, world)
	if code, _, _ := trustRun(t, world, "untrust", "untrust"); code != 0 {
		t.Fatal("the first untrust failed")
	}
	world.trusted = nil // the pool no longer vouches for it
	code, out, errOut = trustRun(t, world, "untrust", "untrust")
	if code != 0 || errOut != "" {
		t.Fatalf("the second huddle untrust = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "not trusted") {
		t.Errorf("the second run does not say the root is not trusted:\n%s", out)
	}
	if world.store.uninstalls != 1 {
		t.Errorf("the store was asked %d times over two runs, want once", world.store.uninstalls)
	}
}

func TestTheTrustWordsRefuseABrokenCAAndEveryWrongShape(t *testing.T) {
	world := newTrustWorld(t)
	// a corrupt root.key is refused before the store is reached
	if code, _, _ := trustRun(t, world, "trust", "trust"); code != 0 {
		t.Fatal("the first trust failed")
	}
	if err := os.WriteFile(filepath.Join(world.home, "pki", "root.key"), []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := trustRun(t, world, "trust", "trust")
	if code != 1 || out != "" || !strings.Contains(errOut, "cannot serve") {
		t.Fatalf("trust over a corrupt root.key = (%d, %q, %q), want 1 naming the CA", code, out, errOut)
	}
	if world.store.installs != 1 {
		t.Errorf("a broken CA still reached the store: %d installs", world.store.installs)
	}
	for _, c := range []struct {
		name string
		word string
		argv []string
	}{
		{"a plain word beside trust", "trust", []string{"trust", "extra"}},
		{"a flag beside trust", "trust", []string{"trust", "--force"}},
		{"a plain word beside untrust", "untrust", []string{"untrust", "extra"}},
		{"a flag beside untrust", "untrust", []string{"untrust", "--all"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := trustRun(t, world, c.word, c.argv...)
			if code != 2 || out != "" || errOut == "" {
				t.Fatalf("huddle %v = (%d, %q, %q), want 2 with the usage", c.argv, code, out, errOut)
			}
			if !strings.Contains(errOut, "usage: huddle trust") {
				t.Errorf("the refusal does not carry the usage:\n%s", errOut)
			}
		})
	}
}

func TestTrustSaysWhyTheStoreRefused(t *testing.T) {
	world := newTrustWorld(t)
	world.store.installErr = errors.New("security: could not write the keychain")
	code, out, errOut := trustRun(t, world, "trust", "trust")
	if code != 1 || !strings.Contains(errOut, "keychain") {
		t.Fatalf("huddle trust = (%d, %q, %q), want 1 with the OS's reason", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(world.home, "pki", "trusted")); !errors.Is(err, os.ErrNotExist) {
		t.Error("a refused install still wrote the trusted marker")
	}
}

// The word needs the world's home: an environment naming none is refused before anything else.
// The world answers empty for every variable, so the real environment is never read.
func TestTheTrustWordsNeedAHome(t *testing.T) {
	empty := &World{Getenv: func(string) string { return "" }}
	var out, errOut bytes.Buffer
	if code := Trust(context.Background(), empty, []string{"trust"}, &out, &errOut); code != 1 || errOut.Len() == 0 {
		t.Fatalf("trust with no home = (%d, %q, %q), want 1", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := Untrust(context.Background(), empty, []string{"untrust"}, &out, &errOut); code != 1 || errOut.Len() == 0 {
		t.Fatalf("untrust with no home = (%d, %q, %q), want 1", code, out.String(), errOut.String())
	}
}
