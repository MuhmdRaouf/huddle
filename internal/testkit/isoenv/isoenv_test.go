package isoenv

// The helper's contract, proved against a planted owner environment: every family that names the
// owner's state is dropped, the child's roots move under home, extra wins, and the machine's
// PATH survives.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
)

// ownerVars plants one variable per family the owner's shell may carry, so every drop is
// observable whatever this machine really exports, and answers the names it planted.
func ownerVars(t *testing.T) []string {
	t.Helper()
	names := []string{
		"XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME",
		"HUDDLE_HOME", "HUDDLE_TOKEN", "HUDDLE_ROUTER_HOME", "RADAR_HOME",
		"AGENTS_HUDDLE_HOME", "CLAUDE_CONFIG_DIR", "ANTHROPIC_API_KEY",
		"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
		"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "TMPDIR",
	}
	for _, name := range names {
		t.Setenv(name, "/owners/"+strings.ToLower(name))
	}
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if !ok {
			t.Fatalf("no compiled provider named %s", name)
		}
		t.Setenv(provider.EnvPrefix+"_API_KEY", "owners-"+name+"-key")
		names = append(names, provider.EnvPrefix+"_API_KEY")
	}
	return names
}

func TestEnvironDropsEveryVariableThatNamesTheOwnersState(t *testing.T) {
	// The home comes first: a planted TMPDIR would point t.TempDir at the owner's path too.
	home := t.TempDir()
	planted := ownerVars(t)
	got := Lookup(Environ(t, home))
	// These six are dropped and then set again under home; for them the planted value must be
	// gone. The rest must be absent outright.
	reset := map[string]bool{
		"XDG_CONFIG_HOME": true, "XDG_STATE_HOME": true, "XDG_DATA_HOME": true,
		"XDG_CACHE_HOME": true, "TMPDIR": true, "CLAUDE_CONFIG_DIR": true,
	}
	for _, name := range planted {
		value, ok := got[name]
		switch {
		case reset[name]:
			if ok && value == "/owners/"+strings.ToLower(name) {
				t.Errorf("%s = %q, want the owner's value replaced by one under home", name, value)
			}
		case ok:
			t.Errorf("%s = %q, want it dropped: the child must not inherit the owner's state", name, value)
		}
	}
}

func TestEnvironRootsTheChildUnderHomeAndKeepsTheMachinesPath(t *testing.T) {
	home := t.TempDir()
	ownerVars(t)
	t.Setenv("LANG", "en_US.UTF-8")
	got := Lookup(Environ(t, home))
	want := map[string]string{
		"HOME":              home,
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"XDG_STATE_HOME":    filepath.Join(home, ".local", "state"),
		"XDG_DATA_HOME":     filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":    filepath.Join(home, ".cache"),
		"TMPDIR":            filepath.Join(home, "tmp"),
		"CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude"),
		"HUDDLE_KEYS":       "files",
	}
	for name, expected := range want {
		if got[name] != expected {
			t.Errorf("%s = %q, want %q", name, got[name], expected)
		}
	}
	if _, err := os.Stat(want["TMPDIR"]); err != nil {
		t.Errorf("TMPDIR: %v", err)
	}
	if got["PATH"] != os.Getenv("PATH") {
		t.Errorf("PATH = %q, want the machine's own %q: the child must find its tools", got["PATH"], os.Getenv("PATH"))
	}
	if got["LANG"] != "en_US.UTF-8" {
		t.Errorf("LANG = %q, want the machine's own value kept", got["LANG"])
	}
}

func TestEnvironExtraWinsWithoutDuplicates(t *testing.T) {
	home := t.TempDir()
	entries := Environ(t, home, "HOME=/elsewhere", "HUDDLE_TOKEN=extra-token", "PATH=/custom/bin")
	got := Lookup(entries)
	if got["HOME"] != "/elsewhere" {
		t.Errorf("HOME = %q, want the extra's /elsewhere", got["HOME"])
	}
	if got["HUDDLE_TOKEN"] != "extra-token" {
		t.Errorf("HUDDLE_TOKEN = %q, want the extra's value", got["HUDDLE_TOKEN"])
	}
	if got["PATH"] != "/custom/bin" {
		t.Errorf("PATH = %q, want the extra's /custom/bin", got["PATH"])
	}
	for _, name := range []string{"HOME", "PATH", "HUDDLE_TOKEN"} {
		if n := namedEntries(entries, name); n != 1 {
			t.Errorf("%s appears %d times in the child's environment, want once: a duplicate hides which value a child reads", name, n)
		}
	}
}

// namedEntries counts the entries whose name is exactly the one given.
func namedEntries(entries []string, name string) int {
	n := 0
	for _, entry := range entries {
		if candidate, _, _ := strings.Cut(entry, "="); candidate == name {
			n++
		}
	}
	return n
}

func TestEnvironNamesTheChildItsOwnRouterPort(t *testing.T) {
	t.Run("unset, the port is a free one in the 20000-29999 range, never the canonical 18786", func(t *testing.T) {
		got := Lookup(Environ(t, t.TempDir()))
		port, err := strconv.Atoi(got["HUDDLE_ROUTER_PORT"])
		if err != nil {
			t.Fatalf("HUDDLE_ROUTER_PORT = %q, want a port number", got["HUDDLE_ROUTER_PORT"])
		}
		if port < 20000 || port > 29999 {
			t.Errorf("HUDDLE_ROUTER_PORT = %d, want a port in 20000-29999", port)
		}
	})

	t.Run("an extra naming the port wins", func(t *testing.T) {
		got := Lookup(Environ(t, t.TempDir(), "HUDDLE_ROUTER_PORT=24500"))
		if got["HUDDLE_ROUTER_PORT"] != "24500" {
			t.Errorf("HUDDLE_ROUTER_PORT = %q, want the extra's 24500", got["HUDDLE_ROUTER_PORT"])
		}
	})
}

func TestInheritedIsTheProcessesOwnEnvironment(t *testing.T) {
	t.Setenv("ISOENV_PROBE", "planted")
	got := Lookup(Inherited())
	if got["ISOENV_PROBE"] != "planted" {
		t.Errorf("Inherited()[ISOENV_PROBE] = %q, want the planted value", got["ISOENV_PROBE"])
	}
}
