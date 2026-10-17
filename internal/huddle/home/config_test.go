package home

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// oneHome is a home under a temp HOME with the environment pointing at it.
func oneHome(t *testing.T) Home {
	t.Helper()
	root := filepath.Join(t.TempDir(), "huddle")
	return Home{Root: root}
}

func TestLoadConfigAnswersDefaultsForAMissingOrTornFile(t *testing.T) {
	h := oneHome(t)
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig with no file: %v", err)
	}
	if cfg.Role != "solo" || cfg.Port != 0 || cfg.PortAuto {
		t.Errorf("defaults = %+v", cfg)
	}
	if cfg.Machine == "" {
		t.Error("the default machine is empty")
	}
	if !cfg.Remote.Permissions || !cfg.Remote.Messages {
		t.Errorf("the remote switches default off: %+v", cfg.Remote)
	}
	if len(cfg.Remote.Allow) == 0 || cfg.Remote.Allow[0] != "*" {
		t.Errorf("the default allow list = %v", cfg.Remote.Allow)
	}
	writeFile(t, h.Config(), "not toml at all [")
	cfg, err = LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig on a torn file: %v", err)
	}
	if cfg.Role != "solo" {
		t.Errorf("a torn file = %+v", cfg)
	}
}

func TestLoadConfigReadsEveryKeyAndKeepsTheTypesStrict(t *testing.T) {
	h := oneHome(t)
	writeFile(t, h.Config(), `role = "hub"
port = 18862
port_auto = true
machine = "laptop"

[hub]
url = "https://huddle.example"
listen = "100.90.0.1"
host = "huddle.example"

[remote]
permissions = false
messages = false
allow = ["Read", "Grep"]

[notify]
push = false
`)
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != "hub" || cfg.Port != 18862 || !cfg.PortAuto || cfg.Machine != "laptop" {
		t.Errorf("cfg = %+v", cfg)
	}
	if cfg.Hub != (HubConfig{URL: "https://huddle.example", Listen: "100.90.0.1", Host: "huddle.example"}) {
		t.Errorf("hub = %+v", cfg.Hub)
	}
	if cfg.Remote.Permissions || cfg.Remote.Messages || len(cfg.Remote.Allow) != 2 || cfg.Remote.Allow[1] != "Grep" {
		t.Errorf("remote = %+v", cfg.Remote)
	}
	// a port written as anything but a number is no port, not a parse crash
	writeFile(t, h.Config(), "port = \"18860\"\n")
	if cfg, err := LoadConfig(h); err != nil || cfg.Port != 0 {
		t.Errorf("a string port = %+v %v", cfg, err)
	}
}

func TestSaveConfigKeepsUnknownKeysAndWritesAtomically(t *testing.T) {
	h := oneHome(t)
	writeFile(t, h.Config(), "favourite = \"tea\"\n\n[owner]\nname = \"a\"\n\n[notify]\npush = false\n")
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	cfg.Role = "satellite"
	cfg.Port = 18865
	if err := SaveConfig(h, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	text := string(readFile(t, h.Config()))
	for _, want := range []string{`favourite = "tea"`, "[owner]", `name = "a"`, `push = false`, `role = "satellite"`, "port = 18865"} {
		if !strings.Contains(text, want) {
			t.Errorf("the rewritten config lost %q:\n%s", want, text)
		}
	}
	if mode(t, h.Config()) != 0o600 {
		t.Errorf("the config is %o, want 0600", mode(t, h.Config()))
	}
	if strings.Contains(text, ".tmp") {
		t.Errorf("a temp file leaked into the home:\n%s", text)
	}
	again, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if again.Role != "satellite" || again.Port != 18865 {
		t.Errorf("round trip = %+v", again)
	}
	entries, err := os.ReadDir(h.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Errorf("a temp file stayed behind: %s", entry.Name())
		}
	}
}

func TestSavePortRecordsTheChoiceAndDropsPortAutoForAChosenPort(t *testing.T) {
	h := oneHome(t)
	writeFile(t, h.Config(), "machine = \"laptop\"\n\n[notify]\npush = true\n")
	if _, err := SavePort(h, 18860, true); err != nil {
		t.Fatalf("SavePort: %v", err)
	}
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Port != 18860 || !cfg.PortAuto || cfg.Machine != "laptop" {
		t.Errorf("after an auto save = %+v", cfg)
	}
	if _, err := SavePort(h, 18870, false); err != nil {
		t.Fatalf("SavePort: %v", err)
	}
	cfg, err = LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Port != 18870 || cfg.PortAuto {
		t.Errorf("after a chosen save = %+v", cfg)
	}
	// the keys the owner wrote are still there
	if text := string(readFile(t, h.Config())); !strings.Contains(text, "push = true") {
		t.Errorf("the rewrite lost the owner's keys:\n%s", text)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestTheCertSectionRoundTripsAndKeepsUnknownKeys(t *testing.T) {
	h := oneHome(t)
	writeFile(t, h.Config(), "favourite = \"tea\"\n\n[cert]\ndns_provider = \"cloudflare\"\n")
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Cert.DNSProvider != "cloudflare" {
		t.Errorf("cert = %+v, want the dns provider", cfg.Cert)
	}
	cfg.Cert.DNSProvider = "desec"
	if err := SaveConfig(h, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	text := string(readFile(t, h.Config()))
	for _, want := range []string{`favourite = "tea"`, `dns_provider = "desec"`} {
		if !strings.Contains(text, want) {
			t.Errorf("the rewritten config lost %q:\n%s", want, text)
		}
	}
	again, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if again.Cert.DNSProvider != "desec" {
		t.Errorf("round trip = %+v", again.Cert)
	}
	// a home with no cert section answers none, and a rewrite keeps the key it read
	fresh := oneHome(t)
	cfg, err = LoadConfig(fresh)
	if err != nil || cfg.Cert.DNSProvider != "" {
		t.Errorf("a fresh home = %+v %v", cfg.Cert, err)
	}
	if err := SaveConfig(fresh, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if cfg, err = LoadConfig(fresh); err != nil || cfg.Cert.DNSProvider != "" {
		t.Errorf("a rewritten fresh home = %+v %v", cfg.Cert, err)
	}
}
