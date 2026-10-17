package cli

// The pairing words: `huddle pair` mints one one-use link and prints it with a QR code and the
// code both sides compare, `huddle devices` lists what paired, and `huddle devices rm` revokes a
// device with every session it holds. The link's host follows the machine's role: the hub's own
// name over https, else this machine's loopback port.

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/huddle/hub"
)

// pairOut runs one pairing word and answers its exit code with both streams.
func pairOut(t *testing.T, w *World, argv []string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Huddle(context.Background(), w, argv, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestPairPrintsTheLinkCodeAndQR pins what the terminal shows: a QR code, the link itself (the
// loopback one in solo, the hub's own name when the config carries it), and the code.
func TestPairPrintsTheLinkCodeAndQR(t *testing.T) {
	homeDir, w := oneHome(t)
	if _, err := home.SavePort(home.Home{Root: homeDir}, 18999, false); err != nil {
		t.Fatalf("save the port: %v", err)
	}

	code, out, errOut := pairOut(t, w, []string{"pair", "--name", "iPhone"})
	if code != 0 {
		t.Fatalf("huddle pair = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "http://localhost:18999/pair#") {
		t.Errorf("the link points elsewhere:\n%s", out)
	}
	if !strings.Contains(out, "Pair this device as iPhone") {
		t.Errorf("the name is missing:\n%s", out)
	}
	if !strings.Contains(out, "▀") && !strings.Contains(out, "█") {
		t.Errorf("no QR code was printed:\n%s", out)
	}
	link := regexp.MustCompile(`http://localhost:18999/pair#([A-Z2-7]{26})`).FindStringSubmatch(out)
	if link == nil {
		t.Fatalf("the link's token is not 26 base32 characters:\n%s", out)
	}
	if want := link[1][:3] + "-" + link[1][3:6]; !strings.Contains(out, want) {
		t.Errorf("the code %s is not on the terminal", want)
	}
	if code, _, errOut := pairOut(t, w, []string{"pair", "--name", "iPad !!"}); code != 2 || errOut == "" {
		t.Errorf("huddle pair with a bad name = %d (%q), want 2 with a usage line", code, errOut)
	}

	// the hub's own name replaces the loopback when the config carries it
	cfg := savedCfg(t, homeDir)
	cfg.Hub.Host = "huddle.box"
	if err := home.SaveConfig(home.Home{Root: homeDir}, cfg); err != nil {
		t.Fatalf("save the config: %v", err)
	}
	code, out, errOut = pairOut(t, w, []string{"pair"})
	if code != 0 {
		t.Fatalf("huddle pair with hub.host = %d: %s", code, errOut)
	}
	if !strings.Contains(out, "https://huddle.box/pair#") {
		t.Errorf("the link does not use the hub's name:\n%s", out)
	}
}

// TestPairNeeds somewhere to point at: with no server, no saved port and no hub name, the word
// refuses with exit 6 rather than print a link nothing answers.
func TestPairNeedsAPort(t *testing.T) {
	_, w := oneHome(t)
	code, _, errOut := pairOut(t, w, []string{"pair"})
	if code != 6 {
		t.Fatalf("huddle pair with nothing to point at = %d (%q), want 6", code, errOut)
	}
	if !strings.Contains(errOut, "huddle up") {
		t.Errorf("the refusal does not say how to get a server: %q", errOut)
	}
}

// TestDevicesListsAndRemoves covers the whole device word: the empty listing, the table with
// its three columns, the removal that ends the sessions, and the removal of what is not there.
func TestDevicesListsAndRemoves(t *testing.T) {
	homeDir, w := oneHome(t)
	ctx := context.Background()

	code, out, errOut := pairOut(t, w, []string{"devices"})
	if code != 0 || !strings.Contains(out, "no devices paired") {
		t.Fatalf("huddle devices on an empty home = %d: %s (%s)", code, out, errOut)
	}

	// one device, paired the way the database does: a link spent on a device with its session
	db, err := hub.Open(ctx, home.Home{Root: homeDir}.HubDB())
	if err != nil {
		t.Fatalf("open hub.db: %v", err)
	}
	now := time.Now().UnixMilli()
	if err := db.SavePairing(ctx, "d1", now+300_000); err != nil {
		t.Fatalf("save the pairing: %v", err)
	}
	if err := db.PairFinish(ctx, "d1", hub.Device{
		Name: "iPhone", Handle: []byte("handle-one"), CredentialJSON: []byte("{}"),
	}, "s1", now+12*3_600_000, now); err != nil {
		t.Fatalf("record the device: %v", err)
	}
	if device, ok, err := db.DeviceSession(ctx, "s1", now); err != nil || !ok || device != "iPhone" {
		t.Fatalf("the device's session = %q %t %v, want iPhone", device, ok, err)
	}

	code, out, errOut = pairOut(t, w, []string{"devices"})
	if code != 0 {
		t.Fatalf("huddle devices = %d: %s (%s)", code, out, errOut)
	}
	for _, want := range []string{"NAME", "ADDED", "LAST SEEN", "iPhone"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing misses %q:\n%s", want, out)
		}
	}

	code, out, errOut = pairOut(t, w, []string{"devices", "rm", "iPhone"})
	if code != 0 || !strings.Contains(out, "iPhone removed") {
		t.Fatalf("huddle devices rm iPhone = %d: %s (%s)", code, out, errOut)
	}
	if device, ok, err := db.DeviceSession(ctx, "s1", time.Now().UnixMilli()); ok || device != "" {
		t.Errorf("the session outlived the removal: %q %t %v", device, ok, err)
	}
	if _, found, _ := db.DeviceByName(ctx, "iPhone"); found {
		t.Error("the device row outlived the removal")
	}

	code, _, errOut = pairOut(t, w, []string{"devices", "rm", "iPhone"})
	if code != 4 {
		t.Fatalf("removing what is not there = %d (%s), want 4", code, errOut)
	}
	code, _, errOut = pairOut(t, w, []string{"devices", "rm"})
	if code != 2 {
		t.Fatalf("rm with no name = %d (%s), want 2", code, errOut)
	}
}

// TestPairRunsFromTheTerminalToo pins the binary's own word: a session-less line reaches the
// same pairing code the CLI's word runs.
func TestPairRunsFromTheTerminalToo(t *testing.T) {
	dir := t.TempDir()
	homeDir := filepath.Join(dir, "home")
	if _, err := home.SavePort(home.Home{Root: homeDir}, 18998, false); err != nil {
		t.Fatalf("save the port: %v", err)
	}
	w := &World{Getenv: func(key string) string {
		if key == "HUDDLE_HOME" {
			return homeDir
		}
		return ""
	}}
	var out, errOut bytes.Buffer
	code := Pair(context.Background(), w, []string{"pair"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("pair alone = %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "http://localhost:18998/pair#") {
		t.Errorf("the alone word prints no loopback link:\n%s", out.String())
	}
}
