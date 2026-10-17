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
