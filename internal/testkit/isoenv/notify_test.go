package isoenv

// A test never reaches the owner's desktop: every environment this package builds, for a child
// (Environ) or for the test process itself (Sandbox), names a notification log under its temp
// home, so a notifier writes a line there instead of showing a banner.

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnvironSendsNotificationsToALogUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HUDDLE_NOTIFY_LOG", "/owners/notify.log")
	if got, want := Lookup(Environ(t, home))["HUDDLE_NOTIFY_LOG"], filepath.Join(home, "notify.log"); got != want {
		t.Errorf("HUDDLE_NOTIFY_LOG = %q, want %q", got, want)
	}
	own := filepath.Join(home, "own.log")
	if got := Lookup(Environ(t, home, "HUDDLE_NOTIFY_LOG="+own))["HUDDLE_NOTIFY_LOG"]; got != own {
		t.Errorf("HUDDLE_NOTIFY_LOG = %q, want the extra's %q", got, own)
	}
}

// sandboxChildEnv makes a re-executed copy of this test binary sandbox itself and report its
// environment instead of running the suite.
const sandboxChildEnv = "ISOENV_TEST_SANDBOX_CHILD"

// TestSandboxChildHelper is the body of the sandboxed process: it prints what Sandbox set and is
// a no-op in a normal run.
func TestSandboxChildHelper(t *testing.T) {
	if os.Getenv(sandboxChildEnv) == "" {
		t.Skip("runs only as the sandboxed process of TestSandboxSendsNotificationsToALogUnderItsHome")
	}
	cleanup, err := Sandbox()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, name := range []string{"HOME", "HUDDLE_NOTIFY_LOG", "PATH"} {
		os.Stdout.WriteString("sandbox-child " + name + "=" + os.Getenv(name) + "\n")
	}
}

// sandboxChild runs the helper under extra and answers the variables it printed.
func sandboxChild(t *testing.T, extra ...string) map[string]string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSandboxChildHelper$")
	cmd.Env = append(os.Environ(), append([]string{sandboxChildEnv + "=1"}, extra...)...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the sandboxed child failed: %v\n%s", err, out)
	}
	printed := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if line, ok := strings.CutPrefix(scanner.Text(), "sandbox-child "); ok {
			name, value, _ := strings.Cut(line, "=")
			printed[name] = value
		}
	}
	return printed
}

func TestSandboxSendsNotificationsToALogUnderItsHome(t *testing.T) {
	got := sandboxChild(t, "HUDDLE_NOTIFY_LOG=/owners/notify.log")
	if got["HOME"] == "" {
		t.Fatalf("the child printed no HOME: %v", got)
	}
	if want := filepath.Join(got["HOME"], "notify.log"); got["HUDDLE_NOTIFY_LOG"] != want {
		t.Errorf("HUDDLE_NOTIFY_LOG = %q, want %q", got["HUDDLE_NOTIFY_LOG"], want)
	}
}
