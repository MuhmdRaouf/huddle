package home

// The certificate the hub listener serves (SPEC 7.2): obtained over DNS-01, laid in the home's
// tls/ with private modes, and kept fresh by a renewal loop that wakes 30 days before expiry and
// retries every 6 hours after a failure — all proved here against a fake issuer and a fake clock,
// because the real issuer talks to Let's Encrypt.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testPair is one self-signed pair shaped the way an issuer answers it.
type testPair struct {
	certPEM  []byte
	keyPEM   []byte
	notAfter time.Time
}

// mintPair answers one self-signed pair valid from now for the duration given.
func mintPair(t *testing.T, lifetime time.Duration) testPair {
	t.Helper()
	pair, err := mintPairAt(time.Now(), lifetime)
	if err != nil {
		t.Fatalf("mint a pair: %v", err)
	}
	return pair
}

// mintPairAt answers one pair valid from at for the duration given: the renewal's fake clock
// mints against itself, so the waits the loop computes land on whole days.
func mintPairAt(at time.Time, lifetime time.Duration) (testPair, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return testPair{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "hub.test"},
		DNSNames:     []string{"hub.test"},
		NotBefore:    at.Add(-time.Hour),
		// certificates carry whole seconds, so the minted expiry is what reads back
		NotAfter: at.Add(lifetime).Truncate(time.Second),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return testPair{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return testPair{}, err
	}
	return testPair{
		certPEM:  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		keyPEM:   pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		notAfter: template.NotAfter,
	}, nil
}

// writePair lays one pair into the home's tls/ the way a certificate on disk looks.
func writePair(t *testing.T, h Home, pair testPair) {
	t.Helper()
	if err := os.MkdirAll(h.TLS(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.TLS(), "cert.pem"), pair.certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.TLS(), "key.pem"), pair.keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeACME is the issuer in a test: what it hands out (minted from the loop's own clock), how
// often it was asked, and the failure it answers instead.
type fakeACME struct {
	from  func() time.Time
	life  time.Duration
	last  testPair
	asked int
	err   error
}

func (f *fakeACME) Obtain(_ context.Context, _ string) ([]byte, []byte, error) {
	f.asked++
	if f.err != nil {
		return nil, nil, f.err
	}
	pair, err := mintPairAt(f.from(), f.life)
	if err != nil {
		return nil, nil, err
	}
	f.last = pair
	return pair.certPEM, pair.keyPEM, nil
}

// fakeClock is the loop's clock: the fake sleeps move it, so the whole renewal runs in no time.
type fakeClock struct {
	now    time.Time
	slept  []time.Duration
	stopAt int
}

// sleep records one wait, moves the clock across it and answers whether the loop goes on; the
// stopAt-th sleep ends the loop, so every test answers without a timeout.
func (c *fakeClock) sleep(_ context.Context, d time.Duration) bool {
	c.slept = append(c.slept, d)
	c.now = c.now.Add(d)
	return len(c.slept) < c.stopAt
}

// renewDeps is one renewal loop's recording world: its clock, its log lines and its issuer.
type renewDeps struct {
	clock  fakeClock
	lines  []string
	issuer func(ctx context.Context) (ACME, error)
}

func (d *renewDeps) options() RenewalOptions {
	return RenewalOptions{
		Now:   func() time.Time { return d.clock.now },
		Sleep: d.clock.sleep,
		Log:   func(line string) { d.lines = append(d.lines, line) },
	}
}

func TestIssueCertificateLaysThePairInTLSPrivately(t *testing.T) {
	h := Home{Root: t.TempDir()}
	acme := &fakeACME{from: func() time.Time { return time.Now() }, life: 90 * 24 * time.Hour}
	expiry, err := IssueCertificate(bg(), h, acme, "hub.test")
	if err != nil {
		t.Fatalf("IssueCertificate: %v", err)
	}
	if !expiry.Equal(acme.last.notAfter) {
		t.Errorf("expiry = %v, want the leaf's %v", expiry, acme.last.notAfter)
	}
	cert, err := os.ReadFile(filepath.Join(h.TLS(), "cert.pem"))
	if err != nil {
		t.Fatalf("cert.pem: %v", err)
	}
	key, err := os.ReadFile(filepath.Join(h.TLS(), "key.pem"))
	if err != nil {
		t.Fatalf("key.pem: %v", err)
	}
	if string(cert) != string(acme.last.certPEM) || string(key) != string(acme.last.keyPEM) {
		t.Error("the pair on disk is not the pair the issuer answered")
	}
	for _, name := range []string{"cert.pem", "key.pem"} {
		info, err := os.Stat(filepath.Join(h.TLS(), name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, info.Mode().Perm())
		}
	}
	if acme.asked != 1 {
		t.Errorf("asked = %d, want 1", acme.asked)
	}
}

func TestCertificateExpiryReadsTheLeafAndAnswersNothingWithoutOne(t *testing.T) {
	h := Home{Root: t.TempDir()}
	if _, err := CertificateExpiry(h); err == nil {
		t.Error("a home with no certificate answers no error")
	}
	pair := mintPair(t, 24*time.Hour)
	writePair(t, h, pair)
	expiry, err := CertificateExpiry(h)
	if err != nil {
		t.Fatalf("CertificateExpiry: %v", err)
	}
	if !expiry.Equal(pair.notAfter) {
		t.Errorf("expiry = %v, want %v", expiry, pair.notAfter)
	}
}

func TestRenewalWaitsUntilThirtyDaysBeforeExpiryThenRenews(t *testing.T) {
	h := Home{Root: t.TempDir()}
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	first, err := mintPairAt(start, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("mint the standing pair: %v", err)
	}
	writePair(t, h, first)
	acme := &fakeACME{from: func() time.Time { return start.Add(60 * 24 * time.Hour) }, life: 90 * 24 * time.Hour}
	deps := &renewDeps{
		clock:  fakeClock{now: start, stopAt: 2},
		issuer: func(context.Context) (ACME, error) { return acme, nil },
	}
	RenewCertificates(bg(), h, deps.issuer, "hub.test", deps.options())
	// the first wait ends 30 days before expiry: 90 days of life minus the 30-day window, and the
	// next cycle waits the same again for the pair the renewal just wrote
	want := []time.Duration{60 * 24 * time.Hour, 60 * 24 * time.Hour}
	if len(deps.clock.slept) != len(want) || deps.clock.slept[0] != want[0] || deps.clock.slept[1] != want[1] {
		t.Fatalf("slept = %v, want %v", deps.clock.slept, want)
	}
	if acme.asked != 1 {
		t.Errorf("asked = %d, want exactly one renewal", acme.asked)
	}
	if len(deps.lines) != 1 {
		t.Fatalf("lines = %d (%v), want the one renewal line", len(deps.lines), deps.lines)
	}
	if !strings.Contains(deps.lines[0], "hub.test") || !strings.Contains(deps.lines[0], "renewed") {
		t.Errorf("line %q does not say what renewed", deps.lines[0])
	}
	cert, _ := os.ReadFile(filepath.Join(h.TLS(), "cert.pem"))
	if string(cert) != string(acme.last.certPEM) {
		t.Error("the renewed pair never landed in tls/")
	}
}

func TestRenewalFiresAtOnceInsideTheWindow(t *testing.T) {
	h := Home{Root: t.TempDir()}
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	first, err := mintPairAt(start, 10*24*time.Hour)
	if err != nil {
		t.Fatalf("mint the standing pair: %v", err)
	}
	writePair(t, h, first)
	acme := &fakeACME{from: func() time.Time { return start }, life: 90 * 24 * time.Hour}
	deps := &renewDeps{
		clock:  fakeClock{now: start, stopAt: 1},
		issuer: func(context.Context) (ACME, error) { return acme, nil },
	}
	RenewCertificates(bg(), h, deps.issuer, "hub.test", deps.options())
	if acme.asked != 1 {
		t.Errorf("asked = %d, want the renewal at once", acme.asked)
	}
	// no wait came before the renewal; the one wait on record is the next cycle's 60 days
	if len(deps.clock.slept) != 1 || deps.clock.slept[0] != 60*24*time.Hour {
		t.Errorf("slept = %v, want no wait before the renewal, then the next cycle's 60 days", deps.clock.slept)
	}
}

func TestRenewalLogsOneLinePerFailureAndRetriesInSixHours(t *testing.T) {
	h := Home{Root: t.TempDir()}
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	first, err := mintPairAt(start, 40*24*time.Hour)
	if err != nil {
		t.Fatalf("mint the standing pair: %v", err)
	}
	writePair(t, h, first)
	acme := &fakeACME{err: errors.New("the DNS provider refused the record")}
	deps := &renewDeps{
		clock:  fakeClock{now: start, stopAt: 4},
		issuer: func(context.Context) (ACME, error) { return acme, nil },
	}
	RenewCertificates(bg(), h, deps.issuer, "hub.test", deps.options())
	if acme.asked != 3 {
		t.Errorf("asked = %d, want three attempts", acme.asked)
	}
	// the 10-day wait into the window, then one 6-hour retry after each of the three failures
	want := []time.Duration{10 * 24 * time.Hour, 6 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	if len(deps.clock.slept) != len(want) {
		t.Fatalf("slept = %v, want %v", deps.clock.slept, want)
	}
	for i, d := range want {
		if deps.clock.slept[i] != d {
			t.Fatalf("slept = %v, want %v", deps.clock.slept, want)
		}
	}
	if len(deps.lines) != 3 {
		t.Fatalf("lines = %d (%v), want one per failure", len(deps.lines), deps.lines)
	}
	for _, line := range deps.lines {
		if !strings.Contains(line, "refused") {
			t.Errorf("line %q does not say why", line)
		}
	}
}

func TestRenewalEndsWithoutACertificateToKeepFresh(t *testing.T) {
	h := Home{Root: t.TempDir()}
	acme := &fakeACME{from: func() time.Time { return time.Now() }, life: 90 * 24 * time.Hour}
	deps := &renewDeps{
		clock:  fakeClock{now: time.Now(), stopAt: 9},
		issuer: func(context.Context) (ACME, error) { return acme, nil },
	}
	RenewCertificates(bg(), h, deps.issuer, "hub.test", deps.options())
	if acme.asked != 0 {
		t.Errorf("asked = %d, want none: the first certificate is huddle cert's to obtain", acme.asked)
	}
	if len(deps.lines) != 1 {
		t.Fatalf("lines = %d (%v), want the one line that says there is nothing to renew", len(deps.lines), deps.lines)
	}
}

func TestRenewalTreatsAnIssuerThatCannotBeBuiltAsOneFailure(t *testing.T) {
	h := Home{Root: t.TempDir()}
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	first, err := mintPairAt(start, 90*24*time.Hour)
	if err != nil {
		t.Fatalf("mint the standing pair: %v", err)
	}
	writePair(t, h, first)
	deps := &renewDeps{
		clock: fakeClock{now: start, stopAt: 2},
		issuer: func(context.Context) (ACME, error) {
			return nil, errors.New("no token for dns:cloudflare in the keystore")
		},
	}
	RenewCertificates(bg(), h, deps.issuer, "hub.test", deps.options())
	if len(deps.clock.slept) != 2 || deps.clock.slept[0] != 60*24*time.Hour || deps.clock.slept[1] != 6*time.Hour {
		t.Fatalf("slept = %v, want the 60-day wait then the 6-hour retry", deps.clock.slept)
	}
	if len(deps.lines) != 1 {
		t.Fatalf("lines = %d (%v), want one", len(deps.lines), deps.lines)
	}
	if !strings.Contains(deps.lines[0], "no token") {
		t.Errorf("line %q does not say why", deps.lines[0])
	}
}
