package isoenv

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Sandbox roots this whole test process at a temp home, for a package's TestMain: code a test
// runs in process (a hook, a CLI word, a wired provider) reads os.Environ, and on a developer
// machine that is the owner's real home, settings and live routers. It drops every variable
// Environ drops, sets HOME, the XDG roots, CLAUDE_CONFIG_DIR, HUDDLE_KEYS=files, a
// HUDDLE_ROUTER_PORT nothing listens on and CLAUDE_PROJECT_DIR=home/project (made: the identity
// lookup walks up from it instead of from the test's cwd, a package directory inside this
// repository), and pins the Go caches where they were, so a build under the new HOME reuses them. Tests that need more set it with t.Setenv as before. The
// answer removes the temp home; call it after m.Run.
func Sandbox() (cleanup func(), err error) {
	keepGo, err := goCaches()
	if err != nil {
		return nil, err
	}
	home, err := os.MkdirTemp("", "isoenv-process-")
	if err != nil {
		return nil, err
	}
	cleanup = func() { _ = os.RemoveAll(home) }
	project := filepath.Join(home, "project")
	if err := os.Mkdir(project, 0o700); err != nil {
		cleanup()
		return nil, err
	}
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if dropped(name) {
			_ = os.Unsetenv(name)
		}
	}
	port, err := unusedPort()
	if err != nil {
		cleanup()
		return nil, err
	}
	set := map[string]string{
		"HOME":               home,
		"XDG_CONFIG_HOME":    filepath.Join(home, ".config"),
		"XDG_STATE_HOME":     filepath.Join(home, ".local", "state"),
		"XDG_DATA_HOME":      filepath.Join(home, ".local", "share"),
		"XDG_CACHE_HOME":     filepath.Join(home, ".cache"),
		"CLAUDE_CONFIG_DIR":  filepath.Join(home, ".claude"),
		"HUDDLE_KEYS":        "files",
		"HUDDLE_ROUTER_PORT": strconv.Itoa(port),
		"CLAUDE_PROJECT_DIR": project,
		sandboxMarker:        home,
	}
	for name, value := range keepGo {
		set[name] = value
	}
	for name, value := range set {
		if err := os.Setenv(name, value); err != nil {
			cleanup()
			return nil, err
		}
	}
	return cleanup, nil
}

// goCaches is where the Go build and module caches are now, computed the way go computes its
// defaults, so moving HOME never sends a build to an empty cache.
func goCaches() (map[string]string, error) {
	keep := map[string]string{}
	if value := os.Getenv("GOCACHE"); value != "" {
		keep["GOCACHE"] = value
	} else {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("isoenv: the Go build cache: %w", err)
		}
		keep["GOCACHE"] = filepath.Join(dir, "go-build")
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("isoenv: GOPATH: %w", err)
		}
		gopath = filepath.Join(home, "go")
	}
	keep["GOPATH"] = gopath
	if value := os.Getenv("GOMODCACHE"); value != "" {
		keep["GOMODCACHE"] = value
	} else {
		keep["GOMODCACHE"] = filepath.Join(strings.Split(gopath, string(os.PathListSeparator))[0], "pkg", "mod")
	}
	return keep, nil
}

// unusedPort is a loopback port the kernel hands out, in its ephemeral range: never 18786.
func unusedPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// sandboxMarker names the home Sandbox made, so RequireSandboxed can tell it from a HOME a
// test set by hand.
const sandboxMarker = "ISOENV_SANDBOX_HOME"

// RequireSandboxed fails a test whose process is not under Sandbox: HOME must be the home
// Sandbox made, and the router port must not be the canonical 18786.
func RequireSandboxed(t testing.TB) {
	t.Helper()
	made := os.Getenv(sandboxMarker)
	if made == "" || os.Getenv("HOME") != made {
		t.Fatalf("HOME = %q, not a sandbox home: call isoenv.Sandbox in this package's TestMain", os.Getenv("HOME"))
	}
	if port := os.Getenv("HUDDLE_ROUTER_PORT"); port == "" || port == "18786" {
		t.Fatalf("HUDDLE_ROUTER_PORT = %q: call isoenv.Sandbox in this package's TestMain", port)
	}
}
