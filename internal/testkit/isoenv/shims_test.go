package isoenv

// The notifier shims: a test process or child built here finds osascript, osacompile and
// notify-send first in a directory of shims that record their argv and fail, so a run that
// starts a notifier is named by tmpguard instead of showing a banner on the owner's desktop.

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInstalledShimsRecordTheirArgvAndFail(t *testing.T) {
	dir := t.TempDir()
	if err := InstallShims(dir); err != nil {
		t.Fatalf("InstallShims: %v", err)
	}
	// The child's PATH is the shim directory alone, so the names resolve to the shims or to
	// nothing: a real notifier cannot run here.
	for _, name := range []string{"osascript", "osacompile", "notify-send"} {
		cmd := exec.Command("/bin/sh", "-c", name+` -e "two words" three`)
		cmd.Env = []string{"PATH=" + filepath.Join(dir, "bin")}
		if err := cmd.Run(); err == nil {
			t.Errorf("%s shim exited 0, want a failure", name)
		}
	}
	got, err := Spawns(dir)
	if err != nil {
		t.Fatalf("Spawns: %v", err)
	}
	want := []string{
		"osascript -e two words three",
		"osacompile -e two words three",
		"notify-send -e two words three",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Spawns = %q, want %q", got, want)
	}
}

func TestSpawnsOfAnEmptyRecordAreNone(t *testing.T) {
	dir := t.TempDir()
	if err := InstallShims(dir); err != nil {
		t.Fatalf("InstallShims: %v", err)
	}
	if got, err := Spawns(dir); err != nil || len(got) != 0 {
		t.Errorf("Spawns = %q, %v, want none", got, err)
	}
}

func TestEnvironPutsTheShimsFirstOnThePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ShimsEnv, dir)
	shims := filepath.Join(dir, "bin")
	want := shims + string(os.PathListSeparator) + os.Getenv("PATH")
	if got := Lookup(Environ(t, t.TempDir()))["PATH"]; got != want {
		t.Errorf("PATH = %q, want %q", got, want)
	}
	if got := Lookup(Environ(t, t.TempDir(), "PATH=/custom/bin"))["PATH"]; got != "/custom/bin" {
		t.Errorf("PATH = %q, want the extra's /custom/bin", got)
	}
}

func TestAChildOfEnvironThatStartsANotifierIsRecorded(t *testing.T) {
	dir := t.TempDir()
	if err := InstallShims(dir); err != nil {
		t.Fatalf("InstallShims: %v", err)
	}
	t.Setenv(ShimsEnv, dir)
	env := Environ(t, t.TempDir())
	if !strings.HasPrefix(Lookup(env)["PATH"], filepath.Join(dir, "bin")+string(os.PathListSeparator)) {
		t.Fatalf("PATH = %q, want the shims first: stopping before a notifier can run", Lookup(env)["PATH"])
	}
	cmd := exec.Command("/bin/sh", "-c", "notify-send Huddle blocked")
	cmd.Env = env
	_ = cmd.Run()
	got, err := Spawns(dir)
	if err != nil || !slices.Equal(got, []string{"notify-send Huddle blocked"}) {
		t.Errorf("Spawns = %q, %v, want the one notify-send", got, err)
	}
}

func TestSandboxPutsTheShimsFirstOnThePath(t *testing.T) {
	dir := t.TempDir()
	got := sandboxChild(t, ShimsEnv+"="+dir)
	if prefix := filepath.Join(dir, "bin") + string(os.PathListSeparator); !strings.HasPrefix(got["PATH"], prefix) {
		t.Errorf("PATH = %q, want it to start with %q", got["PATH"], prefix)
	}
}

func TestGuardNamesALogUnderHomeAndPutsTheShimsFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ShimsEnv, dir)
	in := map[string]string{"HOME": "/h", "PATH": "/usr/bin"}
	got := Guard(in)
	if got["HUDDLE_NOTIFY_LOG"] != "/h/notify.log" {
		t.Errorf("HUDDLE_NOTIFY_LOG = %q, want /h/notify.log", got["HUDDLE_NOTIFY_LOG"])
	}
	if want := filepath.Join(dir, "bin") + string(os.PathListSeparator) + "/usr/bin"; got["PATH"] != want {
		t.Errorf("PATH = %q, want %q", got["PATH"], want)
	}
	if _, named := in["HUDDLE_NOTIFY_LOG"]; named || in["PATH"] != "/usr/bin" {
		t.Errorf("Guard changed its argument: %v", in)
	}
	if got := Guard(map[string]string{"HOME": "/h", "HUDDLE_NOTIFY_LOG": "/own.log"}); got["HUDDLE_NOTIFY_LOG"] != "/own.log" {
		t.Errorf("HUDDLE_NOTIFY_LOG = %q, want the map's own", got["HUDDLE_NOTIFY_LOG"])
	}
	if got := Guard(map[string]string{"PATH": "/usr/bin"}); got["HUDDLE_NOTIFY_LOG"] != "" {
		t.Errorf("HUDDLE_NOTIFY_LOG = %q for a map with no HOME, want none", got["HUDDLE_NOTIFY_LOG"])
	}
	t.Setenv(ShimsEnv, "")
	if got := Guard(in); got["PATH"] != "/usr/bin" {
		t.Errorf("PATH = %q without shims, want it unchanged", got["PATH"])
	}
}
