package pki

// The refusals a broken or locked CA answers: halves that do not belong together, files the
// process cannot read, a directory nothing can be laid into — each a typed answer, never a
// silent replacement of the root machines may already trust.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestThePairHalvesMustBelongTogether(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)}
	one := filepath.Join(t.TempDir(), "pki")
	other := filepath.Join(t.TempDir(), "pki")
	if _, err := New(one, Options{Now: clock.Now, Rand: rand.Reader}).Root(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(other, Options{Now: clock.Now, Rand: rand.Reader}).Root(); err != nil {
		t.Fatal(err)
	}
	// one CA's key under the other's certificate: the mismatch is a refusal
	key, err := os.ReadFile(filepath.Join(other, "root.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(one, "root.key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(one, Options{Now: clock.Now, Rand: rand.Reader}).Root(); err == nil {
		t.Error("a key from another CA served its certificate")
	}
}

func TestAnRSARootIsARefusal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: "an RSA root"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "root.key"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir, Options{Rand: rand.Reader}).Root(); err == nil {
		t.Error("an RSA root served a CA that only mints ECDSA")
	}
}

func TestAKeyWithoutItsCertificateIsARefusal(t *testing.T) {
	authority, _ := newTestAuthority(t)
	if _, err := authority.Root(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(authority.RootFile()); err != nil {
		t.Fatal(err)
	}
	var broken *BrokenCAError
	if _, err := authority.Root(); !errors.As(err, &broken) {
		t.Fatalf("a key without its certificate answers %T (%v), want BrokenCAError", err, err)
	}
}

func TestUnreadableIntermediateHalvesAreAnswers(t *testing.T) {
	for _, c := range []struct {
		name string
		file func(*Authority) string
	}{
		{"an unreadable certificate", (*Authority).IntermediateFile},
		{"an unreadable key", (*Authority).IntermediateKeyFile},
	} {
		t.Run(c.name, func(t *testing.T) {
			authority, _ := newTestAuthority(t)
			if _, _, err := authority.Intermediate(); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(c.file(authority), 0o000); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(c.file(authority), 0o600) })
			if _, _, err := authority.Intermediate(); err == nil {
				t.Error("an unreadable intermediate half still answered")
			}
		})
	}
}

func TestTheMarkerCannotBeDroppedFromAnUnwritableDirectory(t *testing.T) {
	authority, _ := newTestAuthority(t)
	if err := authority.MarkTrusted(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(authority.Dir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(authority.Dir(), 0o700) })
	if err := authority.UnmarkTrusted(); err == nil {
		t.Error("the marker was dropped from a directory nothing can be written to")
	}
	if !authority.TrustedByUs() {
		t.Error("the marker vanished behind a refused removal")
	}
}

func TestARootCannotBeLaidWhereNoDirectoryCanBeMade(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	if _, err := New(filepath.Join(parent, "pki"), Options{Rand: rand.Reader}).Root(); err == nil {
		t.Error("a root was laid under a parent nothing can be created in")
	}
}
