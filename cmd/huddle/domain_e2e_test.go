package main

// `huddle domain` through the built binary, in a temp home and with no network: a fresh home is
// localhost only, a name writes the proxy-flow role and prints the Caddy block for the pinned
// port, and off goes back to loopback.

import (
	"strings"
	"testing"
)

func TestE2EDomainPrintsTheCaddyBlock(t *testing.T) {
	env := aloneEnv(t)

	if code, out, errOut := aloneExec(t, env, "domain"); code != 0 || out != "localhost only\n" || errOut != "" {
		t.Fatalf("domain on a fresh home = (%d, %q, %q), want 0 and localhost only", code, out, errOut)
	}

	code, out, errOut := aloneExec(t, env, "domain", "hub.example.test")
	if code != 0 || errOut != "" {
		t.Fatalf("domain hub.example.test = (%d, %q, %q), want 0 and silence on stderr", code, out, errOut)
	}
	cfg := loadRoleConfig(t, env)
	if cfg.Role != "hub" || cfg.Hub.Host != "hub.example.test" || cfg.Hub.Listen != "" || cfg.PortAuto || cfg.Port == 0 {
		t.Fatalf("the config after domain = %+v, want role hub, the name, no listener and a pinned port", cfg)
	}
	block := "hub.example.test {\n\ttls internal\n\treverse_proxy 127.0.0.1:"
	if !strings.Contains(out, block) || !strings.Contains(out, "\t\theader_up X-Forwarded-Proto https\n\t}\n}\n") {
		t.Fatalf("domain hub.example.test printed no Caddy block:\n%s", out)
	}

	if code, out, errOut := aloneExec(t, env, "domain", "off"); code != 0 || errOut != "" || !strings.Contains(out, "role solo") {
		t.Fatalf("domain off = (%d, %q, %q), want 0 and the solo lines", code, out, errOut)
	}
	if cfg := loadRoleConfig(t, env); cfg.Role != "solo" || !cfg.PortAuto {
		t.Fatalf("the config after domain off = %+v, want solo with the port handed back", cfg)
	}
}
