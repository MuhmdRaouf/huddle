package cli

// The role word: every accepted shape rewrites config.toml and says what the machine now is and
// what the owner does next, every wrong shape answers the one usage line, and a missing or
// unreadable home says why instead of writing.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// roleWorld is one role word's world: a throwaway huddle home.
func roleWorld(t *testing.T, root string) *World {
	t.Helper()
	return world(t, t.TempDir(), map[string]string{
		"HUDDLE_HOME":    root,
		"XDG_STATE_HOME": t.TempDir(),
		"XDG_DATA_HOME":  t.TempDir(),
	})
}

// runRole runs one role word and answers its exit code and both outputs.
func runRole(t *testing.T, w *World, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	code := Role(context.Background(), w, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestRoleSoloWritesTheDefaultRole pins the solo shape: the config keeps the word, and the
// answer says the machine answers loopback only and needs a restart.
func TestRoleSoloWritesTheDefaultRole(t *testing.T) {
	root := t.TempDir()
	code, out, errOut := runRole(t, roleWorld(t, root), "role", "solo")
	if code != 0 || errOut != "" {
		t.Fatalf("role solo = (%d, %q)", code, errOut)
	}
	if !strings.Contains(out, "role solo: this machine's huddle answers loopback only") ||
		!strings.Contains(out, "restart huddle for the role to take effect") {
		t.Errorf("the solo lines:\n%s", out)
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Role != "solo" {
		t.Errorf("the config after solo = %+v (%v)", cfg, err)
	}
}

// TestRoleSoloIsAcceptedWithoutTheWordPrefix pins the bare spelling: a session that dispatches
// the word itself passes only the shape.
func TestRoleSoloIsAcceptedWithoutTheWordPrefix(t *testing.T) {
	if code, _, _ := runRole(t, roleWorld(t, t.TempDir()), "solo"); code != 0 {
		t.Errorf("role without the word prefix = %d", code)
	}
}

// TestRoleHubNeedsAListenAddressAndAName pins the hub shape: a fixed non-loopback address, a
// named host, and the two lines the owner reads before restarting.
func TestRoleHubNeedsAListenAddressAndAName(t *testing.T) {
	root := t.TempDir()
	code, out, _ := runRole(t, roleWorld(t, root),
		"role", "hub", "--listen", "127.0.0.2:8443", "--host", "hub.mine.test")
	if code != 0 {
		t.Fatalf("role hub = %d", code)
	}
	if !strings.Contains(out, "role hub: paired devices and satellites answer on 127.0.0.2:8443, as hub.mine.test") ||
		!strings.Contains(out, "the listener serves the certificate in") {
		t.Errorf("the hub lines:\n%s", out)
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Role != "hub" || cfg.Hub.Listen != "127.0.0.2:8443" || cfg.Hub.Host != "hub.mine.test" {
		t.Errorf("the config after hub = %+v (%v)", cfg, err)
	}
}

// TestRoleHubRefusesEveryIncompleteShape pins the hub refusals: a missing name, a bare address,
// port 0, an out-of-range or non-numeric port, an address where a name belongs, and a name that
// starts with a dash.
func TestRoleHubRefusesEveryIncompleteShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no host and no listen", []string{"role", "hub"}, "usage: huddle role"},
		{"no host", []string{"role", "hub", "--listen", "127.0.0.2:8443"}, "usage: huddle role"},
		{"bare address", []string{"role", "hub", "--listen", "127.0.0.2", "--host", "hub.mine.test"}, "an address and a fixed port"},
		{"port zero", []string{"role", "hub", "--listen", "127.0.0.2:0", "--host", "hub.mine.test"}, "an address and a fixed port"},
		{"port too big", []string{"role", "hub", "--listen", "127.0.0.2:70000", "--host", "hub.mine.test"}, "a port from 1 to 65535"},
		{"port not a number", []string{"role", "hub", "--listen", "127.0.0.2:hub", "--host", "hub.mine.test"}, "a port from 1 to 65535"},
		{"an address as the host", []string{"role", "hub", "--listen", "127.0.0.2:8443", "--host", "127.0.0.2"}, "the hub's name, not an address"},
		{"a name with a leading dash", []string{"role", "hub", "--listen", "127.0.0.2:8443", "--host", "-hub"}, "the hub's name, not an address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runRole(t, roleWorld(t, t.TempDir()), tc.args...)
			if code != 2 || !strings.Contains(errOut, tc.want) {
				t.Errorf("role hub refusal = (%d, %q), want 2 and %q", code, errOut, tc.want)
			}
		})
	}
}

// TestRoleSatellitePointsAtTheHub pins the satellite shape: the hub's https address is kept as
// given, and the answer says this machine still listens on loopback only.
func TestRoleSatellitePointsAtTheHub(t *testing.T) {
	root := t.TempDir()
	code, out, _ := runRole(t, roleWorld(t, root),
		"role", "satellite", "--hub", "https://hub.mine.test:8443")
	if code != 0 || !strings.Contains(out, "role satellite: this machine reaches the hub at https://hub.mine.test:8443") {
		t.Fatalf("role satellite = (%d,\n%s)", code, out)
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Role != "satellite" || cfg.Hub.URL != "https://hub.mine.test:8443" {
		t.Errorf("the config after satellite = %+v (%v)", cfg, err)
	}
}

// TestRoleSatelliteRefusesEveryIncompleteShape pins the satellite refusals: no --hub, a plain
// http address, and an address without a host.
func TestRoleSatelliteRefusesEveryIncompleteShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"no hub", []string{"role", "satellite"}, "usage: huddle role"},
		{"plain http", []string{"role", "satellite", "--hub", "http://hub.mine.test:8443"}, "the hub's https address"},
		{"no host", []string{"role", "satellite", "--hub", "https://"}, "the hub's https address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errOut := runRole(t, roleWorld(t, t.TempDir()), tc.args...)
			if code != 2 || !strings.Contains(errOut, tc.want) {
				t.Errorf("role satellite refusal = (%d, %q), want 2 and %q", code, errOut, tc.want)
			}
		})
	}
}

// TestRoleRefusesAnUnknownShapeAndAMissingOrBrokenHome pins the word's error paths: an unknown
// role and no shape at all answer the usage line, a world without a home says so, and a home
// whose config cannot be read says why.
func TestRoleRefusesAnUnknownShapeAndAMissingOrBrokenHome(t *testing.T) {
	code, _, errOut := runRole(t, roleWorld(t, t.TempDir()), "role", "sidekick")
	if code != 2 || !strings.Contains(errOut, "usage: huddle role") {
		t.Errorf("an unknown role = (%d, %q)", code, errOut)
	}
	code, _, errOut = runRole(t, roleWorld(t, t.TempDir()), "role")
	if code != 2 || !strings.Contains(errOut, "usage: huddle role") {
		t.Errorf("no shape = (%d, %q)", code, errOut)
	}
	noHome := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": "", "HOME": ""})
	code, _, errOut = runRole(t, noHome, "role", "solo")
	if code != 1 || !strings.Contains(errOut, "no home") {
		t.Errorf("no home = (%d, %q)", code, errOut)
	}
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runRole(t, roleWorld(t, broken), "role", "solo")
	if code != 1 || !strings.Contains(errOut, "huddle: ") {
		t.Errorf("a broken config = (%d, %q)", code, errOut)
	}
}
