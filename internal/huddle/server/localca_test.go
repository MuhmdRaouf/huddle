package server

// The hub listener on huddle's own local CA: with no imported pair and no ACME setup, the
// listener issues its leaf from the home's pki/ and a client that trusts only pki/root.crt
// completes a handshake for the hub's name. An imported pair still wins: the local CA is never
// minted behind a certificate the owner put there.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// localCAHome answers a temp home set up as a hub whose listener binds any free port, with the
// certificate files the caller asks for laid before the run.
func localCAHome(t *testing.T, pair bool) string {
	t.Helper()
	dir := t.TempDir()
	config := "role = \"hub\"\n[hub]\nlisten = \"127.0.0.1:0\"\nhost = \"hub.mine.test\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if pair {
		certPEM, keyPEM, _, err := renewPair("hub.mine.test", 90*24*time.Hour)
		if err != nil {
			t.Fatal(err)
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
	}
	return dir
}

// localCAServer runs the server over dir until it is stopped, answering where its words landed.
func localCAServer(t *testing.T, dir string) (*syncBuffer, func()) {
	t.Helper()
	port := compatFreePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	log := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, Options{
			Version: "localca", Port: port, Home: dir, Data: dir, Token: "localca-root",
			AutoCreate: true,
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

// localCAAddress reads the hub listener's address from the server's own line.
func localCAAddress(t *testing.T, log *syncBuffer) string {
	t.Helper()
	re := regexp.MustCompile(`hub listener on https://(127\.0\.0\.1:\d+)`)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(log.joined()); m != nil {
			return m[1]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no hub listener line in the log:\n%s", log.joined())
	return ""
}

// localCAClient answers one HTTP client trusting exactly the certificates handed over.
func localCAClient(t *testing.T, certs ...*x509.Certificate) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	for _, cert := range certs {
		pool.AddCert(cert)
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:    pool,
				ServerName: "hub.mine.test",
				MinVersion: tls.VersionTLS12,
			},
		},
	}
}

func TestTheHubListenerServesItsOwnLeafFromTheLocalCA(t *testing.T) {
	dir := localCAHome(t, false)
	log, stop := localCAServer(t, dir)
	defer stop()
	address := localCAAddress(t, log)
	root := readLocalCARoot(t, dir)
	client := localCAClient(t, root)
	resp, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatalf("a client trusting only pki/root.crt could not handshake: %v\n%s", err, log.joined())
	}
	resp.Body.Close()
	// the handshake answered a leaf that names the hub and chains to the root
	connState := resp.TLS
	if connState == nil || len(connState.PeerCertificates) == 0 {
		t.Fatal("the answer carried no TLS state")
	}
	leaf := connState.PeerCertificates[0]
	if err := leaf.VerifyHostname("hub.mine.test"); err != nil {
		t.Errorf("the served leaf does not name the hub: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tls")); !os.IsNotExist(err) {
		t.Errorf("the local CA path wrote into tls/: %v", err)
	}
}

func TestAnImportedPairWinsOverTheLocalCA(t *testing.T) {
	dir := localCAHome(t, true)
	log, stop := localCAServer(t, dir)
	defer stop()
	address := localCAAddress(t, log)
	// the CA is never minted: no pki/ appears behind the owner's pair
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, "pki")); err == nil {
			t.Fatalf("the local CA was minted behind an imported pair:\n%s", log.joined())
		}
		time.Sleep(20 * time.Millisecond)
	}
	// the pair itself answers: its leaf is what the handshake serves
	certPEM, err := os.ReadFile(filepath.Join(dir, "tls", "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	imported, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	client := localCAClient(t, imported)
	resp, err := client.Get("https://" + address + "/")
	if err != nil {
		t.Fatalf("a client trusting the imported pair could not handshake: %v\n%s", err, log.joined())
	}
	resp.Body.Close()
	if served := resp.TLS.PeerCertificates[0]; !served.Equal(imported) {
		t.Error("the handshake served a leaf other than the imported pair's")
	}
}

// readLocalCARoot answers the root the listener laid in the home's pki/.
func readLocalCARoot(t *testing.T, dir string) *x509.Certificate {
	t.Helper()
	path := filepath.Join(dir, "pki", "root.crt")
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := os.ReadFile(path); err == nil {
			block, _ := pem.Decode(body)
			if block == nil {
				t.Fatalf("pki/root.crt holds no certificate")
			}
			root, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			return root
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the listener never laid %s", path)
	return nil
}
