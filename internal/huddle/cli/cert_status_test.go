package cli

// `huddle cert` with no flag says which certificate the hub serves: the imported pair, the ACME
// one, or the local CA's leaf — with the names, the expiry and, for the local CA, whether this
// machine trusts the root. The pool and the store are the world's fakes, so no real trust store
// is read or written.

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/pki"
)

// statusWorld is one `huddle cert` status run's world: a temp hub home, the pool the trust check
// reads, and what the word answered.
type statusWorld struct {
	w       *World
	home    string
	trusted *x509.Certificate
}

func newStatusWorld(t *testing.T) *statusWorld {
	t.Helper()
	homeDir := t.TempDir()
	cfg := home.Config{Role: "hub", Hub: home.HubConfig{Listen: "127.0.0.2:8443", Host: "hub.mine.test"}}
	if err := home.SaveConfig(home.Home{Root: homeDir}, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	world := &statusWorld{home: homeDir}
	world.w = &World{
		Getenv: func(key string) string {
			if key == "HUDDLE_HOME" {
				return homeDir
			}
			return ""
		},
		SystemPool: func() *x509.CertPool {
			if world.trusted == nil {
				return x509.NewCertPool()
			}
			pool := x509.NewCertPool()
			pool.AddCert(world.trusted)
			return pool
		},
	}
	return world
}

// statusRun runs the bare word and answers its exit code with both streams.
func statusRun(t *testing.T, world *statusWorld) (int, string, string) {
	t.Helper()
	return certRun(t, &certWorld{w: world.w, home: world.home}, "cert")
}

// layImportedPair installs a pair the owner could hold, with its import marker.
func layImportedPair(t *testing.T, world *statusWorld, expires time.Time) {
	t.Helper()
	leaf := mintLeaf(t, "hub.mine.test", expires)
	if err := home.ImportCertificate(home.Home{Root: world.home}, leaf.cert, leaf.key); err != nil {
		t.Fatalf("import: %v", err)
	}
}

func TestCertStatusNamesTheImportedPair(t *testing.T) {
	world := newStatusWorld(t)
	layImportedPair(t, world, time.Date(2027, 1, 16, 12, 0, 0, 0, time.UTC))
	code, out, errOut := statusRun(t, world)
	if code != 0 || errOut != "" {
		t.Fatalf("huddle cert = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	for _, want := range []string{"hub.mine.test", "imported", home.Home{Root: world.home}.TLS(), "2027-01-16", "never renewed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the status does not say %q:\n%s", want, out)
		}
	}
}

func TestCertStatusNamesTheACMECertificate(t *testing.T) {
	world := newStatusWorld(t)
	cfg, err := home.LoadConfig(home.Home{Root: world.home})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Cert.DNSProvider = "fakeprov"
	if err := home.SaveConfig(home.Home{Root: world.home}, cfg); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := statusRun(t, world)
	if code != 0 || errOut != "" {
		t.Fatalf("huddle cert before the first certificate = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "no certificate") || !strings.Contains(out, "--provider fakeprov") {
		t.Errorf("the status does not name the missing first certificate:\n%s", out)
	}
}

func TestCertStatusNamesTheLocalCAAndItsRoot(t *testing.T) {
	world := newStatusWorld(t)
	code, out, errOut := statusRun(t, world)
	if code != 0 || errOut != "" {
		t.Fatalf("huddle cert with no root yet = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "local CA") || !strings.Contains(out, "hub.mine.test, localhost, 127.0.0.1, ::1") {
		t.Errorf("the status does not name the local CA and its four names:\n%s", out)
	}
	if !strings.Contains(out, "no root yet") {
		t.Errorf("the status does not say the root is not created yet:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(world.home, "pki")); !os.IsNotExist(err) {
		t.Errorf("the status minted the CA anyway: %v", err)
	}

	// the root laid and trusted: the status vouches for it
	authority := pki.New(home.Home{Root: world.home}.PKI(), pki.Options{})
	if _, err := authority.Root(); err != nil {
		t.Fatal(err)
	}
	world.trusted = readStatusRoot(t, world)
	code, out, errOut = statusRun(t, world)
	if code != 0 || errOut != "" {
		t.Fatalf("huddle cert with a trusted root = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "trusted by this machine") {
		t.Errorf("the status does not say the root is trusted:\n%s", out)
	}

	// the root no machine vouches for: the status points at the trust word
	world.trusted = nil
	code, out, errOut = statusRun(t, world)
	if code != 0 || errOut != "" {
		t.Fatalf("huddle cert with an untrusted root = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "not trusted yet") || !strings.Contains(out, "huddle trust") ||
		!strings.Contains(out, "https://hub.mine.test:8443") {
		t.Errorf("the status does not point at huddle trust and the hub's address:\n%s", out)
	}
}

func TestCertStatusStillRefusesAMachineWithNoHubName(t *testing.T) {
	world := newStatusWorld(t)
	if err := home.SaveConfig(home.Home{Root: world.home}, home.Config{Role: "solo"}); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := statusRun(t, world)
	if code != 2 || out != "" || !strings.Contains(errOut, "no hub name to certify") {
		t.Fatalf("huddle cert on a solo machine = (%d, %q, %q), want 2 with the shared refusal", code, out, errOut)
	}
}

// readStatusRoot answers the root the test laid in the world's home.
func readStatusRoot(t *testing.T, world *statusWorld) *x509.Certificate {
	t.Helper()
	root, err := pki.New(home.Home{Root: world.home}.PKI(), pki.Options{}).ReadRoot()
	if err != nil {
		t.Fatal(err)
	}
	return root
}
