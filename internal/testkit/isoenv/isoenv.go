// Package isoenv builds the environment a test child runs under: the parent's environment
// without any variable that names the owner's state, rooted at a home of the test's own. Huddle
// resolves its home through XDG_CONFIG_HOME and its state through HOME, so one inherited
// XDG_CONFIG_HOME or HUDDLE_* variable is enough for a test to read — and so write — the real
// ~/.config/huddle or ~/.agents. The guard test in this package keeps every test child
// environment built here.
package isoenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/core/providers"
	"github.com/MuhmdRaouf/huddle/internal/domain"
)

// dropPrefixes is every variable family that names the owner's state: the XDG roots huddle and
// radar resolve their homes through, the huddle, radar and agents variables, Claude Code's and
// Anthropic's, and every compiled provider's own (its keys and state dirs).
var dropPrefixes = func() []string {
	prefixes := []string{"XDG_", "HUDDLE_", "RADAR_", "AGENTS_", "CLAUDE_", "ANTHROPIC_"}
	for _, name := range providers.Names {
		provider, ok := providers.ByName(name)
		if ok {
			prefixes = append(prefixes, provider.EnvPrefix+"_")
		}
	}
	return prefixes
}()

// dropNames names the rest of the owner's state: the proxies that would route a child's
// requests through the owner's network, both spellings, and the temp dir the child gets its own
// of.
var dropNames = []string{
	"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy",
	"ALL_PROXY", "all_proxy", "NO_PROXY", "no_proxy", "TMPDIR",
}

func dropped(name string) bool {
	for _, prefix := range dropPrefixes {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for _, banned := range dropNames {
		if name == banned {
			return true
		}
	}
	return false
}

// Environ answers a child environment rooted at home: the parent's environment without any
// variable that names the owner's state, then HOME=home, the XDG_CONFIG_HOME, XDG_STATE_HOME,
// XDG_DATA_HOME and XDG_CACHE_HOME roots under home, TMPDIR=home/tmp (made), and
// CLAUDE_CONFIG_DIR=home/.claude, HUDDLE_KEYS=files so keys stay in files under the home — and
// finally extra, which wins over all of it. The machine's own PATH, LANG and Go caches pass
// through, so the child finds its tools.
func Environ(t testing.TB, home string, extra ...string) []string {
	t.Helper()
	env := make([]string, 0, 32+len(extra))
	at := make(map[string]int, 32)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if dropped(name) {
			continue
		}
		at[name] = len(env)
		env = append(env, entry)
	}
	if err := os.MkdirAll(filepath.Join(home, "tmp"), 0o700); err != nil {
		t.Fatalf("isoenv: make the child's temp dir under %s: %v", home, err)
	}
	set := func(name, value string) {
		// A repeat names the same slot, so extra wins and no child ever reads a value hidden
		// behind its duplicate.
		entry := name + "=" + value
		if i, ok := at[name]; ok {
			env[i] = entry
			return
		}
		at[name] = len(env)
		env = append(env, entry)
	}
	set("HOME", home)
	set("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	set("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	set("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	set("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	set("TMPDIR", filepath.Join(home, "tmp"))
	set("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	set("HUDDLE_KEYS", "files")
	for _, entry := range extra {
		name, value, _ := strings.Cut(entry, "=")
		set(name, value)
	}
	return env
}

// Inherited is the environment this process inherited, as its parent handed it over. The child
// modes of a re-executed test binary read it to test the environment a spawn really passed; a
// test building a child's environment has Environ.
func Inherited() []string {
	return os.Environ()
}

// Lookup maps one environment's entries; a repeated name keeps its last value.
func Lookup(entries []string) domain.EnvLookup {
	env := make(domain.EnvLookup, len(entries))
	for _, entry := range entries {
		name, value, _ := strings.Cut(entry, "=")
		env[name] = value
	}
	return env
}
