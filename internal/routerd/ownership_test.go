package routerd

// The start's ownership decisions on their own: when two executable paths name one binary, and
// a start that leaves a hub-hosted router alone instead of taking its socket over. The spawns
// here are refused — the suite's binary is never the router — so a row proves the refusal or
// the quiet line, never a daemon.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestSameBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	for _, path := range []string{a, b} {
		if err := os.WriteFile(path, []byte("x"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(a, link); err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(dir, "gone")

	t.Run("equal strings are one binary", func(t *testing.T) {
		if !sameBinary(a, a) {
			t.Error("sameBinary(a, a) = false, want true")
		}
	})
	t.Run("a symlink and its target are one binary", func(t *testing.T) {
		if !sameBinary(link, a) || !sameBinary(a, link) {
			t.Error("sameBinary over a link and its target = false, want true")
		}
	})
	t.Run("two different files are not", func(t *testing.T) {
		if sameBinary(a, b) {
			t.Error("sameBinary(a, b) = true, want false")
		}
	})
	t.Run("a deleted path is never an existing one", func(t *testing.T) {
		if sameBinary(gone, a) || sameBinary(a, gone) {
			t.Error("sameBinary over a deleted path = true, want false")
		}
	})
}

// TestRunningExecutableResolvesItsLink pins the record's name: the binary this process runs
// from is named resolved through its symlinks, so a hub started through the launcher's
// `current` link does not record a name every later build shares; a build the launcher deleted
// keeps its own name.
func TestRunningExecutableResolvesItsLink(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "huddle")
	if err := os.WriteFile(target, []byte("x"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "current")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if link := resolvedExecutable(link); link != mustResolved(t, target) {
		t.Errorf("resolvedExecutable(link) = %q, want the target's own resolved path %q", link, mustResolved(t, target))
	}
	gone := filepath.Join(dir, "gone", "huddle")
	if got := resolvedExecutable(gone); got != gone {
		t.Errorf("resolvedExecutable(deleted) = %q, want the path kept", got)
	}
}

// mustResolved answers path with its symlinks followed, for the expectation side.
func mustResolved(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestUpdatedOrUpLeavesAHostedRouterAlone covers the ownership row: the record says the hub
// hosts the router, its pid is alive, and the start's own binary is another one — the start
// answers the quiet line and spawns nothing. A dead hosted pid is no hub any more, and a
// standalone daemon is still updated: both fall through to the takeover.
func TestUpdatedOrUpLeavesAHostedRouterAlone(t *testing.T) {
	another := "/usr/local/bin/huddle-from-another-build"

	t.Run("a hosted record of a live daemon is the quiet line and no spawn", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		writeStartRecord(t, env, record{PID: os.Getpid(), Port: 1, Executable: another, Hosted: true, Started: "now"})
		line, err := Start(context.Background(), StartOptions{Env: env})
		if err != nil {
			t.Fatalf("Start over a hosted router = %v, want the quiet line", err)
		}
		if line == "" || line[:len("huddle-router: http://127.0.0.1:")] != "huddle-router: http://127.0.0.1:" {
			t.Errorf("Start = %q, want the address line", line)
		}
	})
	t.Run("a hosted record of a dead daemon is a takeover this suite refuses", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		writeStartRecord(t, env, record{PID: int(deadPid(t)), Port: 1, Executable: another, Hosted: true, Started: "now"})
		if _, err := Start(context.Background(), StartOptions{Env: env}); err == nil {
			t.Fatal("Start over a dead hub's record = nil error, want the refused takeover")
		}
	})
	t.Run("a standalone record is a takeover as today", func(t *testing.T) {
		t.Parallel()
		env := startEnv(t)
		healthFake(t, env, http.StatusOK, healthBody("zai"))
		writeStartRecord(t, env, record{PID: 1, Port: 1, Executable: another, Started: "now"})
		if _, err := Start(context.Background(), StartOptions{Env: env}); err == nil {
			t.Fatal("Start over a standalone record = nil error, want the refused takeover")
		}
	})
}
