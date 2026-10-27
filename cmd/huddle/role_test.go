package main

// `huddle role` writes the machine's role into config.toml: hub records the listener's
// address and the hub's name beside it, satellite records the hub it reaches, solo steps back to
// one machine. Every shape the card fixes is refused with one usage line and exit 2.

import (
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// TestRoleWritesTheConfig walks the three roles through the binary and reads back what each one
// wrote.
func TestRoleWritesTheConfig(t *testing.T) {
	env := aloneEnv(t)

	code, out, errOut := aloneExec(t, env, "role", "hub", "--listen", "127.0.0.2:8443", "--host", "hub.mine.test")
	if code != 0 || errOut != "" {
		t.Fatalf("role hub = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	cfg := loadRoleConfig(t, env)
	if cfg.Role != "hub" || cfg.Hub.Listen != "127.0.0.2:8443" || cfg.Hub.Host != "hub.mine.test" {
		t.Fatalf("after role hub the config = %+v, want the hub role with its listener and name", cfg)
	}
	if !strings.Contains(out, "restart") {
		t.Fatalf("role hub said %q, want the line that names the restart", out)
	}

	code, out, errOut = aloneExec(t, env, "role", "satellite", "--hub", "https://hub.mine.test:8443")
	if code != 0 || errOut != "" {
		t.Fatalf("role satellite = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	cfg = loadRoleConfig(t, env)
	if cfg.Role != "satellite" || cfg.Hub.URL != "https://hub.mine.test:8443" {
		t.Fatalf("after role satellite the config = %+v, want the satellite role with its hub", cfg)
	}

	code, out, errOut = aloneExec(t, env, "role", "solo")
	if code != 0 || errOut != "" {
		t.Fatalf("role solo = (%d, %q, %q), want 0 and silence", code, out, errOut)
	}
	if cfg = loadRoleConfig(t, env); cfg.Role != "solo" {
		t.Fatalf("after role solo the config role = %q, want solo", cfg.Role)
	}
}

// TestRoleRefusesEveryWrongShape pins the usage: each wrong line is one usage answer and exit 2,
// and none of them writes a role.
func TestRoleRefusesEveryWrongShape(t *testing.T) {
	lines := [][]string{
		{"role"},
		{"role", "bogus"},
		{"role", "hub"},
		{"role", "hub", "--listen", "127.0.0.2:8443"},
		{"role", "hub", "--listen", "127.0.0.2", "--host", "hub.mine.test"},
		{"role", "hub", "--listen", "127.0.0.2:0", "--host", "hub.mine.test"},
		{"role", "hub", "--listen", "127.0.0.2:99999", "--host", "hub.mine.test"},
		{"role", "hub", "--listen", "127.0.0.2:8443", "--host", "100.64.0.1"},
		{"role", "satellite"},
		{"role", "satellite", "--hub", "http://hub.mine.test:8443"},
		{"role", "satellite", "--hub", "not a url"},
	}
	for _, line := range lines {
		env := aloneEnv(t)
		code, out, errOut := aloneExec(t, env, line...)
		if code != 2 || out != "" || errOut == "" {
			t.Errorf("huddle %v = (%d, %q, %q), want 2 with one usage line", line, code, out, errOut)
		}
		if cfg := loadRoleConfig(t, env); cfg.Role != "solo" {
			t.Errorf("huddle %v wrote role %q, want the config untouched", line, cfg.Role)
		}
	}
}

// loadRoleConfig reads the temp home's config back.
func loadRoleConfig(t *testing.T, env map[string]string) home.Config {
	t.Helper()
	cfg, err := home.LoadConfig(home.Home{Root: env["HUDDLE_HOME"]})
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cfg
}
