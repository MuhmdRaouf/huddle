package cli

// The hub behind a TLS reverse proxy: the role word takes the hub's name alone, the hub keeps
// its loopback listener, and the proxy in front of it owns TLS and the public port.

import (
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
