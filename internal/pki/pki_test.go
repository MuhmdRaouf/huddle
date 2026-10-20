package pki

// The local CA itself: a root with a 10-year life created once and never replaced, an
// intermediate with a 7-day life re-signed by the root at two thirds of it, and leaf
// certificates of 12 hours that name exactly the hub's four names and chain to the root. The
// clock is a fake, so the lifetimes and the renewal points read as arithmetic, and the
// randomness is a seeded reader, so a failure repeats.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeClock is one movable moment every lifetime and renewal point is computed against.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// newTestAuthority answers an authority over a directory nothing has laid yet, with the clock
// and a strong randomness source, plus the clock itself for the moves.
func newTestAuthority(t *testing.T) (*Authority, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	return New(filepath.Join(t.TempDir(), "pki"), Options{Now: clock.Now, Rand: rand.Reader}), clock
}

// readPEMCert parses the first certificate of one of the CA's files.
func readPEMCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	cert, err := parseCertificate(body)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return cert
}

func TestTheRootIsCreatedOnceAndReused(t *testing.T) {
	authority, _ := newTestAuthority(t)
	first, err := authority.Root()
	if err != nil {
		t.Fatalf("the first root: %v", err)
	}
	again, err := authority.Root()
	if err != nil {
		t.Fatalf("the second root: %v", err)
	}
	if !again.Equal(first) {
		t.Error("a second need minted another root")
	}
	info, err := os.Stat(authority.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("the CA directory mode = %o, want 0700", info.Mode().Perm())
	}
	for _, name := range []string{"root.crt", "root.key"} {
		info, err := os.Stat(filepath.Join(authority.Dir(), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, info.Mode().Perm())
		}
	}
	if _, _, err := authority.Intermediate(); err != nil { // its own first need lays it
		t.Fatalf("the first intermediate: %v", err)
	}
	for _, name := range []string{"intermediate.crt", "intermediate.key"} {
		info, err := os.Stat(filepath.Join(authority.Dir(), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, info.Mode().Perm())
		}
	}
	root := readPEMCert(t, authority.RootFile())
	if want := "Huddle Local Authority - 2026 ECC Root"; root.Subject.CommonName != want {
		t.Errorf("the root's name = %q, want %q", root.Subject.CommonName, want)
	}
	if life := root.NotAfter.Sub(root.NotBefore); life != RootLife {
		t.Errorf("the root's life = %v, want %v", life, RootLife)
	}
	rootKey, ok := root.PublicKey.(*ecdsa.PublicKey)
	if !ok || rootKey.Curve != elliptic.P256() {
		t.Error("the root is not an ECDSA P-256 certificate")
	}
	if !root.IsCA || !root.BasicConstraintsValid {
		t.Error("the root carries no CA basic constraints")
	}
}

func TestTheIntermediateIsResignedAtTwoThirdsOfItsLife(t *testing.T) {
	authority, clock := newTestAuthority(t)
	first, _, err := authority.Intermediate()
	if err != nil {
		t.Fatalf("the first intermediate: %v", err)
	}
	if life := first.NotAfter.Sub(first.NotBefore); life != IntermediateLife {
		t.Errorf("the intermediate's life = %v, want %v", life, IntermediateLife)
	}
	root, err := authority.Root()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.CheckSignatureFrom(root); err != nil {
		t.Errorf("the intermediate is not signed by the root: %v", err)
	}
	// two thirds of a 7-day life is 4 days 16 hours: a day short of it keeps the intermediate
	clock.now = clock.now.Add(IntermediateLife * 2 / 3 * 4 / 5)
	same, _, err := authority.Intermediate()
	if err != nil {
		t.Fatal(err)
	}
	if !same.Equal(first) {
		t.Error("the intermediate was re-signed before two thirds of its life was gone")
	}
	// past the point it is re-signed, from the moment the clock names, for the same life again
	clock.now = first.NotBefore.Add(IntermediateLife*2/3 + time.Hour)
	fresh, _, err := authority.Intermediate()
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Equal(first) {
		t.Error("the intermediate past two thirds of its life was kept")
	}
	if err := fresh.CheckSignatureFrom(root); err != nil {
		t.Errorf("the re-signed intermediate is not signed by the root: %v", err)
	}
	if life := fresh.NotAfter.Sub(fresh.NotBefore); life != IntermediateLife {
		t.Errorf("the re-signed intermediate's life = %v, want %v", life, IntermediateLife)
	}
	if start := fresh.NotBefore.Sub(clock.now); start < 0 || start > time.Hour {
		t.Errorf("the re-signed intermediate starts %v from now, want inside the hour", start)
	}
}

func TestTheLeafNamesExactlyTheFourHubNamesAndChainsToTheRoot(t *testing.T) {
	authority, _ := newTestAuthority(t)
	leaf, err := NewLeaf(authority, HubLeafNames("hub.mine.test"))
	if err != nil {
		t.Fatalf("the first leaf: %v", err)
	}
	if len(leaf.cert.Certificate) != 2 {
		t.Fatalf("the leaf is served with %d certificates, want itself and the intermediate", len(leaf.cert.Certificate))
	}
	parsed, err := x509.ParseCertificate(leaf.cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if life := parsed.NotAfter.Sub(parsed.NotBefore); life != LeafLife {
		t.Errorf("the leaf's life = %v, want %v", life, LeafLife)
	}
	wantDNS := []string{"hub.mine.test", "localhost"}
	if len(parsed.DNSNames) != len(wantDNS) {
		t.Fatalf("the leaf's DNS names = %v, want exactly %v", parsed.DNSNames, wantDNS)
	}
	for i, name := range wantDNS {
		if parsed.DNSNames[i] != name {
			t.Errorf("the leaf's DNS name %d = %q, want %q", i, parsed.DNSNames[i], name)
		}
	}
	wantIPs := []string{"127.0.0.1", "::1"}
	if len(parsed.IPAddresses) != len(wantIPs) {
		t.Fatalf("the leaf's IP names = %v, want exactly %v", parsed.IPAddresses, wantIPs)
	}
	for i, ip := range wantIPs {
		if parsed.IPAddresses[i].String() != ip {
			t.Errorf("the leaf's IP name %d = %s, want %s", i, parsed.IPAddresses[i], ip)
		}
	}
	// the roots pool holds only root.crt and the intermediates pool only what the served chain
	// carries: the leaf has to reach the root through the intermediate beside it
	roots := x509.NewCertPool()
	roots.AddCert(readPEMCert(t, authority.RootFile()))
	intermediates := x509.NewCertPool()
	intermediate, err := x509.ParseCertificate(leaf.cert.Certificate[1])
	if err != nil {
		t.Fatal(err)
	}
	intermediates.AddCert(intermediate)
	if _, err := parsed.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		DNSName:       "hub.mine.test",
		CurrentTime:   parsed.NotBefore.Add(time.Minute),
	}); err != nil {
		t.Errorf("the leaf does not chain to the root through the intermediate: %v", err)
	}
	if _, ok := leaf.cert.PrivateKey.(*ecdsa.PrivateKey); !ok {
		t.Error("the leaf's key is not the ECDSA key it was minted with")
	}
}

func TestHubLeafNamesSkipsADuplicateLoopbackName(t *testing.T) {
	names := HubLeafNames("localhost")
	want := []string{"localhost", "127.0.0.1", "::1"}
	if len(names) != len(want) {
		t.Fatalf("HubLeafNames(localhost) = %v, want %v", names, want)
	}
	for i, name := range want {
		if names[i] != name {
			t.Errorf("HubLeafNames(localhost)[%d] = %q, want %q", i, names[i], name)
		}
	}
}

func TestACorruptRootKeyIsATypedRefusalNeverAReplacement(t *testing.T) {
	authority, _ := newTestAuthority(t)
	first, err := authority.Root()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := authority.RootKeyFile()
	if err := os.WriteFile(keyFile, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = authority.Root()
	var broken *BrokenCAError
	if !errors.As(err, &broken) {
		t.Fatalf("a corrupt root.key answers %T (%v), want BrokenCAError", err, err)
	}
	after, readErr := os.ReadFile(keyFile)
	if readErr != nil || string(after) != "not a key" {
		t.Errorf("the corrupt key was rewritten: %v", readErr)
	}
	_, err = authority.Root() // the refusal repeats; nothing was minted behind it
	if !errors.As(err, &broken) {
		t.Fatalf("the second need answers %T (%v), want the same refusal", err, err)
	}
	if !readPEMCert(t, authority.RootFile()).Equal(first) {
		t.Error("the standing root certificate was replaced behind the refusal")
	}
}

func TestAHalfPresentCAIsARefusalToo(t *testing.T) {
	authority, _ := newTestAuthority(t)
	if _, err := authority.Root(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(authority.RootKeyFile()); err != nil {
		t.Fatal(err)
	}
	_, err := authority.Root()
	var broken *BrokenCAError
	if !errors.As(err, &broken) {
		t.Fatalf("a root without its key answers %T (%v), want BrokenCAError", err, err)
	}
}

func TestReadRootAnswersNothingWhenNoCACouldServe(t *testing.T) {
	authority, _ := newTestAuthority(t)
	if _, err := authority.ReadRoot(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadRoot on an empty directory = %T (%v), want os.ErrNotExist", err, err)
	}
	if _, err := os.Stat(authority.RootFile()); !errors.Is(err, os.ErrNotExist) {
		t.Error("ReadRoot created the CA anyway")
	}
}

// The serial numbers come from the injected randomness, so two certificates of one run never
// share one (a big random from a seeded reader proves the plumbing).
func TestSerialsComeFromTheInjectedRandomness(t *testing.T) {
	authority, _ := newTestAuthority(t)
	one, err := authority.Root()
	if err != nil {
		t.Fatal(err)
	}
	if one.SerialNumber.Cmp(big.NewInt(1)) <= 0 {
		t.Errorf("the root's serial = %v, want a large random number", one.SerialNumber)
	}
}
