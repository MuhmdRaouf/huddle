package cli

// The hub behind a TLS reverse proxy: the role word takes the hub's name alone, the hub keeps
// its loopback listener, and the proxy in front of it owns TLS and the public port.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// TestRoleHubWithHostAloneKeepsLoopbackAndNamesTheHost pins the proxy shape: --host without
// --listen is a hub with a name and no listener of its own, and an address as the name is still
// refused.
func TestRoleHubWithHostAloneKeepsLoopbackAndNamesTheHost(t *testing.T) {
	root := t.TempDir()
	code, out, errOut := runRole(t, roleWorld(t, root), "role", "hub", "--host", "hub.example.invalid")
	if code != 0 || errOut != "" {
		t.Fatalf("role hub --host alone = (%d, %q), want 0 and silence", code, errOut)
	}
	if !strings.Contains(out, "role hub: this machine answers loopback only, as https://hub.example.invalid behind a TLS reverse proxy") {
		t.Errorf("the hub lines:\n%s", out)
	}
	cfg, err := home.LoadConfig(home.Home{Root: root})
	if err != nil || cfg.Role != "hub" || cfg.Hub.Host != "hub.example.invalid" || cfg.Hub.Listen != "" {
		t.Errorf("the config after hub --host alone = %+v (%v)", cfg, err)
	}

	code, _, errOut = runRole(t, roleWorld(t, t.TempDir()), "role", "hub", "--host", "127.0.0.2")
	if code != 2 || !strings.Contains(errOut, "the hub's name, not an address") {
		t.Errorf("role hub --host 127.0.0.2 = (%d, %q), want 2 and the name refusal", code, errOut)
	}
	code, _, errOut = runRole(t, roleWorld(t, t.TempDir()), "role", "hub")
	if code != 2 || !strings.Contains(errOut, roleUsage) {
		t.Errorf("role hub without --host = (%d, %q), want 2 and the usage line", code, errOut)
	}
}

// TestRoleHubPinsThePort pins the port a proxy forwards to: role hub behind a proxy records the
// port as chosen (port_auto false), 18860 on a home with none saved, the saved one otherwise.
func TestRoleHubPinsThePort(t *testing.T) {
	root := t.TempDir()
	if code, _, errOut := runRole(t, roleWorld(t, root), "role", "hub", "--host", "hub.example.invalid"); code != 0 {
		t.Fatalf("role hub --host = (%d, %q)", code, errOut)
	}
	if cfg := savedCfg(t, root); cfg.PortAuto || cfg.Port != 18860 {
		t.Errorf("a fresh home after role hub = port %d, auto %v; want 18860 pinned", cfg.Port, cfg.PortAuto)
	}

	saved := t.TempDir()
	writeFile(t, filepath.Join(saved, "config.toml"), "port = 18863\nport_auto = true\n")
	if code, _, errOut := runRole(t, roleWorld(t, saved), "role", "hub", "--host", "hub.example.invalid"); code != 0 {
		t.Fatalf("role hub --host = (%d, %q)", code, errOut)
	}
	if cfg := savedCfg(t, saved); cfg.PortAuto || cfg.Port != 18863 {
		t.Errorf("a home with port 18863 after role hub = port %d, auto %v; want 18863 pinned", cfg.Port, cfg.PortAuto)
	}
}

// TestLeavingTheHubRoleUnpinsThePort pins the way back: solo or satellite after a hub behind its
// proxy hands the port back to huddle (port_auto true, the number kept), while a port someone
// chose on a machine that was never that hub stays chosen.
func TestLeavingTheHubRoleUnpinsThePort(t *testing.T) {
	for _, leave := range [][]string{
		{"role", "solo"},
		{"role", "satellite", "--hub", "https://hub.example.invalid:8443"},
	} {
		root := t.TempDir()
		if code, _, errOut := runRole(t, roleWorld(t, root), "role", "hub", "--host", "hub.example.invalid"); code != 0 {
			t.Fatalf("role hub --host = (%d, %q)", code, errOut)
		}
		if code, _, errOut := runRole(t, roleWorld(t, root), leave...); code != 0 {
			t.Fatalf("%v = (%d, %q)", leave, code, errOut)
		}
		if cfg := savedCfg(t, root); !cfg.PortAuto || cfg.Port != 18860 {
			t.Errorf("after %v = port %d, auto %v; want 18860 handed back to huddle", leave, cfg.Port, cfg.PortAuto)
		}
	}

	chosen := t.TempDir()
	writeFile(t, filepath.Join(chosen, "config.toml"), "port = 18865\n")
	if code, _, errOut := runRole(t, roleWorld(t, chosen), "role", "solo"); code != 0 {
		t.Fatalf("role solo = (%d, %q)", code, errOut)
	}
	if cfg := savedCfg(t, chosen); cfg.PortAuto || cfg.Port != 18865 {
		t.Errorf("a chosen port after role solo = port %d, auto %v; want 18865 still chosen", cfg.Port, cfg.PortAuto)
	}
}
