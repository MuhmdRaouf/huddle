package cli

// `huddle cert` (SPEC 7.2): the terminal word that obtains the hub's certificate over DNS-01.
// The token is asked for with the terminal's echo off, kept in the keystore under
// `dns:<provider>` and never printed; the provider and the hub's name come from the line and
// config.toml, and the issuer is a fake here — the real one talks to Let's Encrypt.

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// fakeSecrets is the keystore in a test: one map, set before the run or read after it.
type fakeSecrets struct {
	held   map[string]string
	setErr map[string]error
}

func (f *fakeSecrets) Get(_ context.Context, account string) (string, bool) {
	value, ok := f.held[account]
	return value, ok
}

func (f *fakeSecrets) Set(_ context.Context, account, value string) error {
	if err, ok := f.setErr[account]; ok {
		return err
	}
	if f.held == nil {
		f.held = map[string]string{}
	}
	f.held[account] = value
	return nil
}

// fakeIssuer is the ACME the tests answer with: one self-signed pair for any host.
type fakeIssuer struct {
	notAfter time.Time
	asked    []string
	err      error
}

func (f *fakeIssuer) Obtain(_ context.Context, host string) ([]byte, []byte, error) {
	f.asked = append(f.asked, host)
	if f.err != nil {
		return nil, nil, f.err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     f.notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// certWorld is one `huddle cert` run's world: a temp home set up as a hub, the fakes it reaches
// and what it answered. known nil knows every name.
type certWorld struct {
	w      *World
	home   string
	store  *fakeSecrets
	issuer *fakeIssuer
	gotPro string
	gotTok string
}

func newCertWorld(t *testing.T, stdin io.Reader) *certWorld {
	t.Helper()
	homeDir := t.TempDir()
	cfg := home.Config{Role: "hub", Hub: home.HubConfig{Listen: "127.0.0.2:8443", Host: "hub.mine.test"}}
	if err := home.SaveConfig(home.Home{Root: homeDir}, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	world := &certWorld{home: homeDir, store: &fakeSecrets{}, issuer: &fakeIssuer{notAfter: time.Now().Add(90 * 24 * time.Hour)}}
	world.w = &World{
		Getenv: func(key string) string {
			if key == "HUDDLE_HOME" {
				return homeDir
			}
			return ""
		},
		Stdin: stdin,
		Secrets: func(context.Context, home.Home) home.SecretStore {
			return world.store
		},
		DNSKnown: func(name string) bool {
			return name == "fakeprov"
		},
		CertIssue: func(provider, token, _ string) (home.ACME, error) {
			world.gotPro, world.gotTok = provider, token
			return world.issuer, nil
		},
	}
	return world
}

// certRun runs the word once and answers its exit code with both streams.
func certRun(t *testing.T, world *certWorld, argv ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Cert(context.Background(), world.w, argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

// testToken is the token the fake terminal hands over; every assertion that a stream or a store
// holds it (or must not) names this constant.
const testToken = "dns-token-0123456789abcdef"

// mintedLeaf is one leaf a test mints the way a file from mkcert or a company CA looks: signed by
// its own CA, the leaf alone and the chain (leaf first) beside its key.
type mintedLeaf struct {
	cert    []byte
	chain   []byte
	key     []byte
	expires time.Time
}

// mintLeaf answers one leaf for host, signed by a fresh CA and valid until expires.
func mintLeaf(t *testing.T, host string, expires time.Time) mintedLeaf {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the CA key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("mint the CA: %v", err)
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse the CA: %v", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate the leaf key: %v", err)
	}
	// a leaf that expired before it began is no leaf: an expired test pair starts an hour earlier
	notBefore := time.Now().Add(-time.Hour)
	if expires.Before(notBefore) {
		notBefore = expires.Add(-time.Hour)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    notBefore,
		NotAfter:     expires,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("mint the leaf: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal the leaf key: %v", err)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return mintedLeaf{
		cert:    cert,
		chain:   append(append([]byte{}, cert...), caPEM...),
		key:     pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		expires: expires,
	}
}

// writeImportFiles lays one minted leaf on disk as the pair to import and answers both paths.
func writeImportFiles(t *testing.T, dir string, certPEM, keyPEM []byte) (string, string) {
	t.Helper()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func TestCertImportInstallsThePairTheOwnerHolds(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(""))
	// a fixed noon UTC, so the date the certificate carries reads back the same in any zone
	expires := time.Date(2027, 1, 16, 12, 0, 0, 0, time.UTC)
	leaf := mintLeaf(t, "hub.mine.test", expires)
	certPath, keyPath := writeImportFiles(t, t.TempDir(), leaf.cert, leaf.key)
	code, out, errOut := certRun(t, world, "cert", "--import", certPath, keyPath)
	if code != 0 {
		t.Fatalf("huddle cert --import = %d: %s%s", code, out, errOut)
	}
	h := home.Home{Root: world.home}
	cert, err := os.ReadFile(filepath.Join(h.TLS(), "cert.pem"))
	if err != nil {
		t.Fatalf("cert.pem: %v", err)
	}
	key, err := os.ReadFile(filepath.Join(h.TLS(), "key.pem"))
	if err != nil {
		t.Fatalf("key.pem: %v", err)
	}
	if string(cert) != string(leaf.cert) || string(key) != string(leaf.key) {
		t.Error("the pair in tls/ is not the pair handed over")
	}
	marker, err := os.ReadFile(filepath.Join(h.TLS(), "imported"))
	if err != nil {
		t.Fatalf("imported: %v", err)
	}
	if string(marker) != "hub.mine.test" {
		t.Errorf("the marker holds %q, want the hub's name", marker)
	}
	for _, name := range []string{"cert.pem", "key.pem", "imported"} {
		info, err := os.Stat(filepath.Join(h.TLS(), name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, info.Mode().Perm())
		}
	}
	if !strings.Contains(out, "hub.mine.test") || !strings.Contains(out, h.TLS()) ||
		!strings.Contains(out, leaf.expires.Format(time.DateOnly)) {
		t.Errorf("the answer does not name the host, the tls directory and the expiry:\n%s", out)
	}
	if strings.Contains(out+errOut, string(leaf.key)) {
		t.Error("the key's bytes reached a stream")
	}
}

func TestCertImportKeepsAChainWhole(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(""))
	leaf := mintLeaf(t, "hub.mine.test", time.Now().Add(90*24*time.Hour).Truncate(time.Second))
	certPath, keyPath := writeImportFiles(t, t.TempDir(), leaf.chain, leaf.key)
	code, out, errOut := certRun(t, world, "cert", "--import", certPath, keyPath)
	if code != 0 {
		t.Fatalf("huddle cert --import = %d: %s%s", code, out, errOut)
	}
	cert, err := os.ReadFile(filepath.Join(home.Home{Root: world.home}.TLS(), "cert.pem"))
	if err != nil {
		t.Fatalf("cert.pem: %v", err)
	}
	if string(cert) != string(leaf.chain) {
		t.Error("the chain on disk is not the file as it was handed over")
	}
}

func TestCertImportRefusesAPairThatCannotServeTheHub(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(""))
	good := mintLeaf(t, "hub.mine.test", time.Now().Add(90*24*time.Hour))
	other := mintLeaf(t, "elsewhere.test", time.Now().Add(90*24*time.Hour))
	expired := mintLeaf(t, "hub.mine.test", time.Now().Add(-24*time.Hour))
	cases := []struct {
		name    string
		certPEM []byte
		keyPEM  []byte
		refusal string
	}{
		{"a key from another pair", good.cert, other.key, "key"},
		{"a leaf for another host", other.cert, other.key, "elsewhere.test"},
		{"an expired leaf", expired.cert, expired.key, "expire"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			certPath, keyPath := writeImportFiles(t, t.TempDir(), c.certPEM, c.keyPEM)
			code, out, errOut := certRun(t, world, "cert", "--import", certPath, keyPath)
			if code != 1 || out != "" || errOut == "" {
				t.Fatalf("huddle cert --import = (%d, %q, %q), want 1 naming why", code, out, errOut)
			}
			if !strings.Contains(errOut, c.refusal) {
				t.Errorf("the refusal %q does not name %q", errOut, c.refusal)
			}
			if _, err := os.Stat(filepath.Join(world.home, "tls")); !os.IsNotExist(err) {
				t.Errorf("a refused pair wrote into tls anyway: %v", err)
			}
			if strings.Contains(out+errOut, string(c.keyPEM)) {
				t.Error("the key's bytes reached a stream")
			}
		})
	}
}

func TestCertImportNeedsTheHubsNameFirst(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(""))
	if err := home.SaveConfig(home.Home{Root: world.home}, home.Config{Role: "solo"}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	leaf := mintLeaf(t, "hub.mine.test", time.Now().Add(90*24*time.Hour))
	certPath, keyPath := writeImportFiles(t, t.TempDir(), leaf.cert, leaf.key)
	code, out, errOut := certRun(t, world, "cert", "--import", certPath, keyPath)
	if code != 2 || out != "" || errOut == "" {
		t.Fatalf("huddle cert --import = (%d, %q, %q), want 2 with one line", code, out, errOut)
	}
	if !strings.Contains(errOut, "no hub name to certify") || !strings.Contains(errOut, "role hub") {
		t.Errorf("the refusal does not match the ACME path's:\n%s", errOut)
	}
	if _, err := os.Stat(filepath.Join(world.home, "tls")); !os.IsNotExist(err) {
		t.Errorf("a refused line wrote into tls anyway: %v", err)
	}
}

func TestCertImportRefusesEveryWrongShape(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(""))
	leaf := mintLeaf(t, "hub.mine.test", time.Now().Add(90*24*time.Hour))
	certPath, keyPath := writeImportFiles(t, t.TempDir(), leaf.cert, leaf.key)
	cases := []struct {
		name string
		argv []string
	}{
		{"no files", []string{"cert", "--import"}},
		{"one file", []string{"cert", "--import", certPath}},
		{"three files", []string{"cert", "--import", certPath, keyPath, certPath}},
		{"beside --provider", []string{"cert", "--import", certPath, keyPath, "--provider", "fakeprov"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := certRun(t, world, c.argv...)
			if code != 2 || out != "" || errOut == "" {
				t.Fatalf("huddle %v = (%d, %q, %q), want 2 with the usage", c.argv, code, out, errOut)
			}
			if !strings.Contains(errOut, "usage: huddle cert") {
				t.Errorf("the refusal does not carry the usage:\n%s", errOut)
			}
			if _, err := os.Stat(filepath.Join(world.home, "tls")); !os.IsNotExist(err) {
				t.Errorf("a refused line wrote into tls anyway: %v", err)
			}
			if world.gotPro != "" || len(world.issuer.asked) != 0 {
				t.Errorf("a refused line reached the issuer: %q %v", world.gotPro, world.issuer.asked)
			}
		})
	}
}

func TestCertObtainsTheCertificateAndStoresTheToken(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(testToken+"\n"))
	code, out, errOut := certRun(t, world, "cert", "--provider", "fakeprov")
	if code != 0 {
		t.Fatalf("huddle cert = %d: %s%s", code, out, errOut)
	}
	if world.gotPro != "fakeprov" || world.gotTok != testToken {
		t.Errorf("the issuer was asked for (%q, %q)", world.gotPro, world.gotTok)
	}
	if got, ok := world.store.Get(context.Background(), "dns:fakeprov"); !ok || got != testToken {
		t.Errorf("the keystore holds %q, %v; want the token under dns:fakeprov", got, ok)
	}
	h := home.Home{Root: world.home}
	for _, name := range []string{"cert.pem", "key.pem"} {
		info, err := os.Stat(filepath.Join(h.TLS(), name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", name, info.Mode().Perm())
		}
	}
	cfg, err := home.LoadConfig(h)
	if err != nil || cfg.Cert.DNSProvider != "fakeprov" {
		t.Errorf("the config = %+v %v, want the provider remembered", cfg.Cert, err)
	}
	// the token is never printed, logged or echoed — the two streams are all the word says
	if strings.Contains(out, testToken) || strings.Contains(errOut, testToken) {
		t.Errorf("the token reached a stream:\n%s%s", out, errOut)
	}
	if !strings.Contains(out, "fakeprov") {
		t.Errorf("the prompt does not name the provider:\n%s", out)
	}
	if !strings.Contains(out, "hub.mine.test") || !strings.Contains(out, h.TLS()) {
		t.Errorf("the answer does not name the host and the tls directory:\n%s", out)
	}
}

func TestCertReusesTheStoredTokenWithoutAsking(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(""))
	world.store.held = map[string]string{"dns:fakeprov": "kept-token-987654321"}
	code, out, _ := certRun(t, world, "cert", "--provider", "fakeprov")
	if code != 0 {
		t.Fatalf("huddle cert = %d", code)
	}
	if world.gotTok != "kept-token-987654321" {
		t.Errorf("the issuer was handed %q, want the stored token", world.gotTok)
	}
	if strings.Contains(out, "token for fakeprov") {
		t.Errorf("a stored token was asked for again:\n%s", out)
	}
}

func TestCertRefusesEveryUsageShapeBeforeAnythingIsTyped(t *testing.T) {
	hubHome := func(t *testing.T) *certWorld { return newCertWorld(t, strings.NewReader(testToken+"\n")) }
	soloHome := func(t *testing.T) *certWorld {
		world := hubHome(t)
		if err := home.SaveConfig(home.Home{Root: world.home}, home.Config{Role: "solo"}); err != nil {
			t.Fatalf("SaveConfig: %v", err)
		}
		return world
	}
	cases := []struct {
		name  string
		world *certWorld
		argv  []string
	}{
		{"no provider", hubHome(t), []string{"cert"}},
		{"a provider the issuer does not know", hubHome(t), []string{"cert", "--provider", "nope"}},
		{"a provider that is not a name", hubHome(t), []string{"cert", "--provider", "not a name"}},
		{"a second word", hubHome(t), []string{"cert", "extra", "--provider", "fakeprov"}},
		{"no hub name to certify", soloHome(t), []string{"cert", "--provider", "fakeprov"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errOut := certRun(t, c.world, c.argv...)
			if code != 2 || out != "" || errOut == "" {
				t.Fatalf("huddle %v = (%d, %q, %q), want 2 with one usage line", c.argv, code, out, errOut)
			}
			if strings.Contains(out+errOut, "token") {
				t.Errorf("a refused line still asks for the token:\n%s%s", out, errOut)
			}
			if _, err := os.Stat(filepath.Join(c.world.home, "tls")); !os.IsNotExist(err) {
				t.Errorf("a refused line wrote a certificate anyway: %v", err)
			}
			if c.world.gotPro != "" || c.world.gotTok != "" || len(c.world.issuer.asked) != 0 {
				t.Errorf("a refused line reached the issuer: %q %q %v", c.world.gotPro, c.world.gotTok, c.world.issuer.asked)
			}
			if _, ok := c.world.store.Get(context.Background(), "dns:fakeprov"); ok {
				t.Error("a refused line stored a token")
			}
		})
	}
}

func TestCertRefusesAnEmptyToken(t *testing.T) {
	world := newCertWorld(t, strings.NewReader("\n"))
	code, out, errOut := certRun(t, world, "cert", "--provider", "fakeprov")
	if code != 2 || out == "" {
		t.Fatalf("huddle cert with an empty token = (%d, %q, %q), want 2", code, out, errOut)
	}
	if _, ok := world.store.Get(context.Background(), "dns:fakeprov"); ok {
		t.Error("an empty token was stored")
	}
	if _, err := os.Stat(filepath.Join(world.home, "tls", "cert.pem")); !os.IsNotExist(err) {
		t.Error("an empty token still wrote a certificate")
	}
}

func TestCertSaysWhyTheIssuerFailed(t *testing.T) {
	world := newCertWorld(t, strings.NewReader(testToken+"\n"))
	world.issuer.err = fmt.Errorf("the DNS provider refused the record")
	code, _, errOut := certRun(t, world, "cert", "--provider", "fakeprov")
	if code != 1 || !strings.Contains(errOut, "refused") {
		t.Fatalf("huddle cert = %d (%q), want 1 naming why", code, errOut)
	}
	if strings.Contains(errOut, testToken) {
		t.Errorf("the failure line carries the token:\n%s", errOut)
	}
}
