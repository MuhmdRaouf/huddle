package cli

// `huddle role hub` ends with the trust line when the local CA serves the listener and nothing
// vouches for the root: the one pointer a fresh hub machine needs, and the only thing the role
// ever says about trust — the word itself is the owner's to run.

import (
	"crypto/x509"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/pki"
)

// trustPoolWorld answers a role world whose system pool holds exactly the roots named.
func trustPoolWorld(t *testing.T, trusted ...*x509.Certificate) *World {
	t.Helper()
	w := roleWorld(t, t.TempDir())
	w.SystemPool = func() *x509.CertPool {
		pool := x509.NewCertPool()
		for _, root := range trusted {
			pool.AddCert(root)
		}
		return pool
	}
	return w
}

func TestRoleHubPointsAtTheTrustWord(t *testing.T) {
	w := trustPoolWorld(t)
	root := t.TempDir()
	w.Getenv = func(key string) string {
		if key == "HUDDLE_HOME" {
			return root
		}
		return ""
	}
	code, out, errOut := runRole(t, w, "role", "hub", "--listen", "127.0.0.2:8443", "--host", "hub.mine.test")
	if code != 0 || errOut != "" {
		t.Fatalf("role hub = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if !strings.Contains(out, "run `huddle trust` once so browsers accept https://hub.mine.test:8443") {
		t.Errorf("the hub role does not end with the trust line:\n%s", out)
	}
	// the role itself never writes a CA or a store: only the pointer is given
	if _, err := os.Stat(home.Home{Root: root}.PKI()); !os.IsNotExist(err) {
		t.Errorf("the role minted the CA itself: %v", err)
	}
}

func TestRoleHubStaysQuietOnceTheRootIsTrusted(t *testing.T) {
	root := t.TempDir()
	authority := pki.New(home.Home{Root: root}.PKI(), pki.Options{})
	if _, err := authority.Root(); err != nil {
		t.Fatal(err)
	}
	rootCert, err := authority.ReadRoot()
	if err != nil {
		t.Fatal(err)
	}
	w := trustPoolWorld(t, rootCert)
	w.Getenv = func(key string) string {
		if key == "HUDDLE_HOME" {
			return root
		}
		return ""
	}
	code, out, errOut := runRole(t, w, "role", "hub", "--listen", "127.0.0.2:8443", "--host", "hub.mine.test")
	if code != 0 || errOut != "" {
		t.Fatalf("role hub = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if strings.Contains(out, "huddle trust") {
		t.Errorf("a trusted root still earned the trust line:\n%s", out)
	}
}

func TestRoleHubStaysQuietForACMEAndImports(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, root string)
	}{
		{"an ACME setup", func(t *testing.T, root string) {
			cfg, err := home.LoadConfig(home.Home{Root: root})
			if err != nil {
				t.Fatal(err)
			}
			cfg.Cert.DNSProvider = "fakeprov"
			if err := home.SaveConfig(home.Home{Root: root}, cfg); err != nil {
				t.Fatal(err)
			}
		}},
		{"an imported pair", func(t *testing.T, root string) {
			if err := home.SaveConfig(home.Home{Root: root}, home.Config{
				Role: "hub",
				Hub:  home.HubConfig{Listen: "127.0.0.2:8443", Host: "hub.mine.test"},
			}); err != nil {
				t.Fatal(err)
			}
			leaf := mintLeaf(t, "hub.mine.test", time.Date(2027, 1, 16, 12, 0, 0, 0, time.UTC))
			if err := home.ImportCertificate(home.Home{Root: root}, leaf.cert, leaf.key); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			c.setup(t, root)
			w := trustPoolWorld(t)
			w.Getenv = func(key string) string {
				if key == "HUDDLE_HOME" {
					return root
				}
				return ""
			}
			code, out, errOut := runRole(t, w, "role", "hub", "--listen", "127.0.0.2:8443", "--host", "hub.mine.test")
			if code != 0 || errOut != "" {
				t.Fatalf("role hub = (%d, %q, %q), want 0 and silence", code, out, errOut)
			}
			if strings.Contains(out, "huddle trust") {
				t.Errorf("a hub not served by the local CA still earned the trust line:\n%s", out)
			}
		})
	}
}
