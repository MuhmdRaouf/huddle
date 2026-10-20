package pki

// The trust decisions: TrustedBy reads only the pool it is handed, and the trusted marker says
// which removals belong to huddle. The real store (SystemStore) has no test on purpose — no test
// may touch the machine's real trust stores.

import (
	"crypto/rand"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// testRoot answers one standing root, minted through the authority itself.
func testRoot(t *testing.T, authority *Authority) *x509.Certificate {
	t.Helper()
	root, err := authority.Root()
	if err != nil {
		t.Fatalf("mint the root: %v", err)
	}
	return root
}

func TestTrustedByReadsOnlyThePool(t *testing.T) {
	// the wall clock: TrustedBy verifies at now, so the root must be live against it
	authority := New(filepath.Join(t.TempDir(), "pki"), Options{Rand: rand.Reader})
	root := testRoot(t, authority)
	if TrustedBy(nil, root) {
		t.Error("a nil pool vouched for the root")
	}
	if TrustedBy(x509.NewCertPool(), root) {
		t.Error("an empty pool vouched for the root")
	}
	pool := x509.NewCertPool()
	pool.AddCert(root)
	if !TrustedBy(pool, root) {
		t.Error("the pool holding the root did not vouch for it")
	}
	// an unrelated CA in the pool vouches for nothing
	other := New(filepath.Join(t.TempDir(), "pki"), Options{Rand: rand.Reader})
	if TrustedBy(pool, testRoot(t, other)) {
		t.Error("a pool vouched for a root it does not hold")
	}
}

func TestTheTrustedMarkerRoundTrips(t *testing.T) {
	authority := New(filepath.Join(t.TempDir(), "pki"), Options{Rand: rand.Reader})
	testRoot(t, authority)
	if authority.TrustedByUs() {
		t.Error("a fresh CA claims huddle installed its root")
	}
	if err := authority.MarkTrusted(); err != nil {
		t.Fatalf("mark: %v", err)
	}
	if !authority.TrustedByUs() {
		t.Error("the marker did not land")
	}
	info, err := os.Stat(filepath.Join(authority.Dir(), "trusted"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("the marker's mode = %o, want 0600", info.Mode().Perm())
	}
	if err := authority.UnmarkTrusted(); err != nil {
		t.Fatalf("unmark: %v", err)
	}
	if authority.TrustedByUs() {
		t.Error("the marker survived its removal")
	}
	// removing an absent marker is fine: an uninstall path may run twice
	if err := authority.UnmarkTrusted(); err != nil {
		t.Errorf("unmarking again = %v, want nil", err)
	}
}

// TestTheRealStoreRefusesInsideATest pins the guard the incident showed was needed: a world
// that forgets its fake store reaches SystemStore, and inside a test that answers a refusal
// before any store is touched.
func TestTheRealStoreRefusesInsideATest(t *testing.T) {
	var off StoreOffError
	if err := (SystemStore{}).Install(nil); !errors.As(err, &off) {
		t.Fatalf("SystemStore.Install in a test = %v, want StoreOffError", err)
	}
	if err := (SystemStore{}).Uninstall(nil); !errors.As(err, &off) {
		t.Fatalf("SystemStore.Uninstall in a test = %v, want StoreOffError", err)
	}
}
