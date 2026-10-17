package server

// The hub role's certificate renewal (SPEC 7.2) as the running server proves it: a hub whose
// certificate sits inside the 30-day window renews it from the server itself, whatever its
// listener did — an address it cannot bind costs the listener, never the fresh certificate —
// while a solo machine with the same config touches nothing. The issuer is a fake: the real one
// talks to Let's Encrypt over DNS-01.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// renewPair answers one self-signed pair for the hub's name, valid from now for the lifetime.
func renewPair(name string, lifetime time.Duration) (certPEM, keyPEM []byte, notAfter time.Time, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(4),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(lifetime),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		template.NotAfter, nil
}

// renewHome answers a temp home set up as the role given, with a certificate that has lifetime
// left and the DNS provider the renewal would use written into config.toml. The listener's
// address is one no process can bind, so the renewal is proven on its own: it runs for the hub
// role whatever the listener did.
func renewHome(t *testing.T, role string, lifetime time.Duration) (string, string) {
	t.Helper()
	dir := t.TempDir()
	config := "role = \"" + role + "\"\n[hub]\nlisten = \"127.0.0.1:1\"\nhost = \"hub.mine.test\"\n\n[cert]\ndns_provider = \"fakeprov\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, _, err := renewPair("hub.mine.test", lifetime)
	if err != nil {
		t.Fatalf("mint the standing pair: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tls"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls", "cert.pem"), certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tls", "key.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, string(certPEM)
}

// renewServer runs one server over the home whose certificate the fake issuer renews, until it
// is stopped; it answers where the server's own words landed.
func renewServer(t *testing.T, dir string) (*syncBuffer, func()) {
	t.Helper()
	issuer := &renewIssuer{}
	port := compatFreePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	log := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Version: "renew", Port: port, Home: dir, Data: dir, Token: "renew-root",
			AutoCreate: true,
			CertACME:   func(context.Context) (home.ACME, error) { return issuer, nil },
			Stdout:     log,
			Stderr:     io.Discard,
		})
	}()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Errorf("the server never stopped")
		}
	}
	return log, stop
}

// renewIssuer answers one fresh pair per ask.
type renewIssuer struct{ asked int }

func (f *renewIssuer) Obtain(_ context.Context, host string) ([]byte, []byte, error) {
	f.asked++
	certPEM, keyPEM, _, err := renewPair(host, 90*24*time.Hour)
	return certPEM, keyPEM, err
}

func TestTheHubRoleRenewsItsCertificateFromTheServer(t *testing.T) {
	dir, standing := renewHome(t, "hub", 240*time.Hour) // 10 days left: inside the 30-day window
	log, stop := renewServer(t, dir)
	defer stop()
	certFile := filepath.Join(dir, "tls", "cert.pem")
	if !until(15*time.Second, func() bool {
		cert, err := os.ReadFile(certFile)
		return err == nil && string(cert) != standing
	}) {
		t.Fatalf("the certificate was never renewed: %s", log.joined())
	}
	if !until(5*time.Second, func() bool {
		return strings.Contains(log.joined(), "renewed")
	}) {
		t.Errorf("no renewal line in the log:\n%s", log.joined())
	}
	key, err := os.ReadFile(filepath.Join(dir, "tls", "key.pem"))
	if err != nil || len(key) == 0 {
		t.Errorf("the renewed key never landed: %v", err)
	}
	// the listener it could not bind is said with one line, and the renewal ran regardless; the
	// line comes from another goroutine than the renewal's, so it is waited for, not assumed
	if !until(5*time.Second, func() bool {
		return strings.Contains(log.joined(), "loopback only")
	}) {
		t.Errorf("the refused listener is not said:\n%s", log.joined())
	}
}

func TestASoloMachineRenewsNothing(t *testing.T) {
	dir, standing := renewHome(t, "solo", 240*time.Hour)
	log, stop := renewServer(t, dir)
	defer stop()
	// a renewal that wrongly started would land at once (the certificate is inside the window)
	time.Sleep(500 * time.Millisecond)
	cert, err := os.ReadFile(filepath.Join(dir, "tls", "cert.pem"))
	if err != nil || string(cert) != standing {
		t.Fatalf("a solo machine's certificate changed: %v", err)
	}
	if strings.Contains(log.joined(), "renewed") {
		t.Errorf("a solo machine logged a renewal:\n%s", log.joined())
	}
}
