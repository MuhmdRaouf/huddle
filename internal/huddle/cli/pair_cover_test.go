package cli

// The pairing words' quiet edges: a world without a home, a home whose config or hub database
// cannot be opened, and the port the pairing link points at.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// runPair runs one pairing word and answers its exit code and both outputs.
func runPair(t *testing.T, w *World, word string, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut strings.Builder
	var code int
	if word == "pair" {
		code = Pair(context.Background(), w, args, &out, &errOut)
	} else {
		code = Devices(context.Background(), w, args, &out, &errOut)
	}
	return code, out.String(), errOut.String()
}

// TestPairAndDevicesNameTheMissingHome pins the words over a world that names no home: both say
// what to set instead of failing quietly.
func TestPairAndDevicesNameTheMissingHome(t *testing.T) {
	noHome := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": "", "HOME": ""})
	code, _, errOut := runPair(t, noHome, "pair")
	if code != 1 || !strings.Contains(errOut, "no home") {
		t.Errorf("pair without a home = (%d, %q)", code, errOut)
	}
	code, _, errOut = runPair(t, noHome, "devices", "devices")
	if code != 1 || !strings.Contains(errOut, "no home") {
		t.Errorf("devices without a home = (%d, %q)", code, errOut)
	}
}

// TestPairAndDevicesSayWhyTheHomeCannotBeRead pins the broken config: every shape of both words
// that reads config.toml first says why it cannot.
func TestPairAndDevicesSayWhyTheHomeCannotBeRead(t *testing.T) {
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, "config.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": broken})
	if code, _, errOut := runPair(t, w, "pair"); code != 1 || !strings.Contains(errOut, "huddle: ") {
		t.Errorf("pair over a broken config = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runPair(t, w, "devices", "devices"); code != 1 || !strings.Contains(errOut, "huddle: ") {
		t.Errorf("devices over a broken config = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runPair(t, w, "devices", "devices", "rm", "phone"); code != 1 || !strings.Contains(errOut, "huddle: ") {
		t.Errorf("devices rm over a broken config = (%d, %q)", code, errOut)
	}
}

// TestPairAndDevicesSayWhyTheHubDatabaseCannotBeOpened pins the broken hub database: a
// directory where hub.db belongs stops pairing and both devices shapes with one explanation.
func TestPairAndDevicesSayWhyTheHubDatabaseCannotBeOpened(t *testing.T) {
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, "hub.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := world(t, t.TempDir(), map[string]string{"HUDDLE_HOME": broken})
	if _, err := home.SavePort(home.Home{Root: broken}, 18860, false); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := runPair(t, w, "pair"); code != 1 || !strings.Contains(errOut, "hub database") {
		t.Errorf("pair over a broken hub database = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runPair(t, w, "devices", "devices"); code != 1 || !strings.Contains(errOut, "hub database") {
		t.Errorf("devices over a broken hub database = (%d, %q)", code, errOut)
	}
	if code, _, errOut := runPair(t, w, "devices", "devices", "rm", "phone"); code != 1 || !strings.Contains(errOut, "hub database") {
		t.Errorf("devices rm over a broken hub database = (%d, %q)", code, errOut)
	}
}

// TestPairPortPrefersTheSavedPortThenTheRunningServer pins the link's port: a chosen port wins,
// a running server's port comes next, and nothing running means no link.
func TestPairPortPrefersTheSavedPortThenTheRunningServer(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	w := cliWorld(t, root, t.TempDir(), nil)
	if got := pairPort(ctx, w, root); got != 0 {
		t.Errorf("pairPort with nothing saved or running = %d", got)
	}
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "run", "huddle.pid"), "4242")
	w.Proc = Proc{
		Command:  func(pid int) (string, bool) { return "huddle serve", true },
		ListenOf: func(pid int) (int, bool) { return 4321, true },
	}
	if got := pairPort(ctx, w, root); got != 4321 {
		t.Errorf("pairPort over a running server = %d", got)
	}
}

// TestWhenOfTrimsADevicesMomentToTheMinute pins the listing's timestamps: a full stamp is cut to
// its minute, a short one stays as it is.
func TestWhenOfTrimsADevicesMomentToTheMinute(t *testing.T) {
	if got := whenOf("2026-10-15T12:34:56.789Z"); got != "2026-10-15T12:34Z" {
		t.Errorf("whenOf(full) = %q", got)
	}
	if got := whenOf("short"); got != "short" {
		t.Errorf("whenOf(short) = %q", got)
	}
}
