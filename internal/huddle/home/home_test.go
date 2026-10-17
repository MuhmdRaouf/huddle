package home

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// envOf is one environment as a lookup, with the same empty-means-unset rule as Resolve.
func envOf(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestResolveWalksHUDDLEHomeThenXDGConfigHomeThenHOME(t *testing.T) {
	// HUDDLE_HOME wins over everything
	h, err := Resolve(envOf(map[string]string{
		"HUDDLE_HOME": "/elsewhere", "XDG_CONFIG_HOME": "/xdg", "HOME": "/Users/a",
	}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if h.Root != "/elsewhere" {
		t.Errorf("Root = %q, want /elsewhere", h.Root)
	}
	// then $XDG_CONFIG_HOME/huddle
	h, err = Resolve(envOf(map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/Users/a"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.FromSlash("/xdg/huddle"); h.Root != want {
		t.Errorf("Root = %q, want %q", h.Root, want)
	}
	// then ~/.config/huddle
	h, err = Resolve(envOf(map[string]string{"HOME": "/Users/a"}))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if want := filepath.Join("/Users/a", ".config", "huddle"); h.Root != want {
		t.Errorf("Root = %q, want %q", h.Root, want)
	}
	// an empty variable counts as unset, and nothing set is an error, never a guess
	if _, err := Resolve(envOf(map[string]string{"HUDDLE_HOME": "", "HOME": ""})); err == nil {
		t.Error("Resolve with no HOME and no HUDDLE_HOME answers no error")
	}
}

func TestThePathsFollowTheLayout(t *testing.T) {
	h := Home{Root: filepath.FromSlash("/one/home")}
	for name, got := range map[string]string{
		"config":    h.Config(),
		"hub db":    h.HubDB(),
		"channels":  h.Channels(),
		"sessions":  h.Sessions(),
		"outbox":    h.Outbox(),
		"imports":   h.Imports(),
		"service":   h.Service(),
		"run":       h.Run(),
		"tls":       h.TLS(),
		"keys":      h.Keys(),
		"auth":      h.Auth(),
		"knowledge": h.Knowledge(),
		"pid":       h.PidFile(),
		"port":      h.PortFile(),
		"log":       h.LogFile(),
	} {
		if !filepath.IsAbs(got) || filepath.Base(filepath.Dir(got)) == "" {
			t.Errorf("%s = %q, want a path under the home", name, got)
		}
	}
	if want := filepath.Join(h.Root, "config.toml"); h.Config() != want {
		t.Errorf("Config = %q, want %q", h.Config(), want)
	}
	if want := filepath.Join(h.Root, "channels", "shop.db"); h.ChannelDB("shop") != want {
		t.Errorf("ChannelDB = %q, want %q", h.ChannelDB("shop"), want)
	}
	if want := filepath.Join(h.Root, "run", "huddle.pid"); h.PidFile() != want {
		t.Errorf("PidFile = %q, want %q", h.PidFile(), want)
	}
	if want := filepath.Join(h.Root, "run", "huddle.port"); h.PortFile() != want {
		t.Errorf("PortFile = %q, want %q", h.PortFile(), want)
	}
	if want := filepath.Join(h.Root, "run", "huddle.log"); h.LogFile() != want {
		t.Errorf("LogFile = %q, want %q", h.LogFile(), want)
	}
	if want := filepath.Join(h.Root, "service.json"); h.Service() != want {
		t.Errorf("Service = %q, want %q", h.Service(), want)
	}
}

// mode is one path's permission bits, whatever the platform did to them.
func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestEnsureCreatesTheTreeOnceAndNeverChangesItAgain(t *testing.T) {
	ctx := context.Background()
	h := Home{Root: filepath.Join(t.TempDir(), "huddle")}
	if err := h.Ensure(ctx); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if mode(t, h.Root) != 0o700 {
		t.Errorf("the home is %o, want 0700", mode(t, h.Root))
	}
	for _, dir := range []string{h.Channels(), h.Sessions(), h.Run(), h.Keys()} {
		if mode(t, dir) != 0o700 {
			t.Errorf("%s is %o, want 0700", dir, mode(t, dir))
		}
	}
	for _, file := range []string{h.Config(), h.Outbox(), h.Imports()} {
		if mode(t, file) != 0o600 {
			t.Errorf("%s is %o, want 0600", file, mode(t, file))
		}
	}
	// a config with defaults came with it
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != "solo" || cfg.Port != 0 || cfg.Machine == "" {
		t.Errorf("the default config = %+v", cfg)
	}
	if !cfg.Remote.Permissions || !cfg.Remote.Messages || len(cfg.Remote.Allow) == 0 {
		t.Errorf("the default remote switches = %+v", cfg.Remote)
	}
	for _, want := range []string{`role = "solo"`, "[hub]", "[remote]", "[notify]", "machine = "} {
		if !strings.Contains(string(readFile(t, h.Config())), want) {
			t.Errorf("the default config lacks %q:\n%s", want, readFile(t, h.Config()))
		}
	}
	// a second Ensure changes nothing: modes stay, the owner's edits stay
	own := []byte("role = \"hub\"\n\n[notify]\npush = false\n")
	if err := os.WriteFile(h.Config(), own, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mode(t, h.Config())
	if err := h.Ensure(ctx); err != nil {
		t.Fatalf("Ensure again: %v", err)
	}
	if string(readFile(t, h.Config())) != string(own) {
		t.Errorf("Ensure rewrote the config to:\n%s", readFile(t, h.Config()))
	}
	if mode(t, h.Config()) != before {
		t.Errorf("the config's mode changed to %o", mode(t, h.Config()))
	}
	// a home made by hand with wider modes keeps them: Ensure never tightens what exists
	if err := os.Chmod(h.Run(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := h.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if mode(t, h.Run()) != 0o755 {
		t.Errorf("Ensure tightened run/ to %o", mode(t, h.Run()))
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}
