package home

// The config file's edges: a file that cannot be read is named, a rewrite that cannot land is
// named, an empty role and machine fall back to their defaults, and the TOML writer refuses
// before anything half-written is left behind.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigNamesAFileItCannotRead(t *testing.T) {
	h := oneHome(t)
	if err := os.MkdirAll(h.Config(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(h); err == nil {
		t.Fatal("LoadConfig read a directory")
	}
}

func TestLoadConfigFillsAFileThatNamesEmptyKeys(t *testing.T) {
	h := oneHome(t)
	writeFile(t, h.Config(), "role = \"\"\nmachine = \"\"\n")
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != "solo" || cfg.Machine == "" {
		t.Errorf("the empty keys read = %+v, want the defaults filled in", cfg)
	}
}

func TestSaveConfigFillsAnEmptyRoleAndMachineWithTheDefaults(t *testing.T) {
	h := oneHome(t)
	if err := SaveConfig(h, Config{Port: 18862}); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != "solo" || cfg.Port != 18862 || cfg.Machine == "" {
		t.Errorf("the saved config = %+v, want solo, the port and a machine", cfg)
	}
}

func TestSaveConfigRewritesATornFileWhole(t *testing.T) {
	h := oneHome(t)
	writeFile(t, h.Config(), "not toml at all [")
	if err := SaveConfig(h, Config{Role: "hub"}); err != nil {
		t.Fatalf("SaveConfig on a torn file: %v", err)
	}
	cfg, err := LoadConfig(h)
	if err != nil || cfg.Role != "hub" {
		t.Errorf("the rewritten file reads = %+v (%v), want hub", cfg, err)
	}
}

func TestSaveConfigNamesAFileItCannotWrite(t *testing.T) {
	h := oneHome(t)
	if err := os.MkdirAll(h.Config(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(h, Config{Role: "hub"}); err == nil {
		t.Fatal("SaveConfig wrote over a directory")
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

func TestSavePortNamesAFileItCannotWrite(t *testing.T) {
	h := oneHome(t)
	if err := os.MkdirAll(h.Config(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SavePort(h, 18860, true); err == nil {
		t.Fatal("SavePort wrote over a directory")
	}
}

func TestSavePortKeepsTheRoleAndMachineAFileNeverNamed(t *testing.T) {
	h := oneHome(t)
	if _, err := SavePort(h, 18860, false); err != nil {
		t.Fatalf("SavePort: %v", err)
	}
	cfg, err := LoadConfig(h)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Role != "solo" || cfg.Machine == "" || cfg.Port != 18860 || cfg.PortAuto {
		t.Errorf("the saved port = %+v, want solo with a machine on a chosen port", cfg)
	}
}

func TestTheRemoteTableKeepsItsAllowListAndDefaultsAnEmptyOne(t *testing.T) {
	kept := remoteTable(RemoteConfig{Permissions: true, Messages: false, Allow: []string{"Read", "Grep"}})
	allow, _ := kept["allow"].([]string)
	if len(allow) != 2 || allow[0] != "Read" || allow[1] != "Grep" {
		t.Errorf("allow = %v, want the list as it came", allow)
	}
	// an empty list reads as the floor's star, never as nothing
	empty := remoteTable(RemoteConfig{})
	if allow, _ := empty["allow"].([]string); len(allow) != 1 || allow[0] != "*" {
		t.Errorf("an empty allow list = %v, want the star", empty["allow"])
	}
	if empty["permissions"] != false || empty["messages"] != false {
		t.Errorf("the switches = %v/%v, want false/false", empty["permissions"], empty["messages"])
	}
}

func TestWriteTOMLNamesItsFailures(t *testing.T) {
	dir := t.TempDir()
	// a value the encoder cannot spell refuses before anything is written
	if err := writeTOML(filepath.Join(dir, "config.toml"), map[string]any{"x": make(chan int)}); err == nil {
		t.Error("a channel encoded as TOML")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.toml")); err == nil {
		t.Error("a failed encode left a file behind")
	}
	// the parent chain crosses a file
	blocked := filepath.Join(dir, "f")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeTOML(filepath.Join(blocked, "sub", "config.toml"), map[string]any{"x": 1}); err == nil {
		t.Error("a write under a file succeeded")
	}
}

func TestEnsureConfigRefusesAConfigItCannotStat(t *testing.T) {
	root := filepath.Join(t.TempDir(), "huddle")
	if err := os.WriteFile(root, []byte("a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureConfig(Home{Root: root}); err == nil {
		t.Error("ensureConfig laid a config under a file")
	}
}

func TestEnsureOnAHomeWhoseConfigIsADirectoryLeavesItAlone(t *testing.T) {
	h := oneHome(t)
	if err := os.MkdirAll(h.Config(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := h.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	info, err := os.Stat(h.Config())
	if err != nil || !info.IsDir() {
		t.Errorf("the config = %v, want the directory left as it was", err)
	}
}
