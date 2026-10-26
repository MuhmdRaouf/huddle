package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	huddlehome "github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// testVersion is the version the tests pass to Run; the binary passes the one
// it was built with.
const testVersion = "dev"

// usageLine is the usage text an unknown command prints, naming the word the
// huddle binary runs radar with.
const usageLine = "usage: huddle radar start|stop|status|url|open|watch [--port N] [--since 24h]"

// cliUsageLine is the usage line the radar cli prints for help.
const cliUsageLine = "usage: radar start|stop|status|url|open|watch [--port N] [--since 24h]"

// notRunningLine is what a wanted-but-absent server prints.
const notRunningLine = "radar: the huddle hub is not running"

// huddleBin is the huddle binary TestMain builds once; the spawn case starts it for real.
var huddleBin string

// TestMain builds the binary the spawn case starts, so it exercises the exact entry the
// plugin will run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "radar-app-binary-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "radar app tests: %v\n", err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "huddle")
	build := exec.Command("go", "build", "-o", bin, "./cmd/huddle")
	build.Dir = "../../.."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building ./cmd/huddle: %v\n%s", err, out)
		os.Exit(1)
	}
	huddleBin = bin
	code := m.Run()
	// os.Exit would skip a defer, so the build dir goes here: the suite must leave no temp behind.
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestRun(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		isolateState bool // the hook records into the state dir it resolves, so give it a temp one
		wantCode     int
		wantStdout   string
		wantStderr   string
	}{
		{"--version prints the version and exits 0", []string{"--version"}, false, 0, "radar dev\n", ""},
		{"no args is an unknown command", nil, false, 1, "", "radar: unknown command: (none)\n" + usageLine + "\n"},
		{"unknown subcommand", []string{"frobnicate"}, false, 1, "", "radar: unknown command: frobnicate\n" + usageLine + "\n"},
		{"hook is silent and exits 0", []string{"hook"}, true, 0, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.isolateState {
				t.Setenv("RADAR_HOME", t.TempDir())
			}
			var stdout, stderr bytes.Buffer
			code := Run(tt.args, &stdout, &stderr, testVersion)
			if code != tt.wantCode {
				t.Errorf("Run(%q) = %d, want %d", tt.args, code, tt.wantCode)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("Run(%q) stdout = %q, want %q", tt.args, stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("Run(%q) stderr = %q, want %q", tt.args, stderr.String(), tt.wantStderr)
			}
		})
	}
}

// TestRunVersionIsTheOneTheCallerPasses pins the version flow: Run prints the
// version its caller hands it, never one of its own.
func TestRunVersionIsTheOneTheCallerPasses(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"--version"}, &stdout, &stderr, "9.9.9"); code != 0 {
		t.Fatalf("Run(--version) = %d, want 0", code)
	}
	if stdout.String() != "radar 9.9.9\n" {
		t.Errorf("stdout = %q, want %q", stdout.String(), "radar 9.9.9\n")
	}
}

// TestEntriesAreTheCLIEntryPoints pins the table to the CLI's entry points: the hook and every
// subcommand it accepts.
func TestEntriesAreTheCLIEntryPoints(t *testing.T) {
	want := []string{"hook", "start", "stop", "status", "url", "open", "watch", "help", "--help", "-h"}
	if len(entries) != len(want) {
		t.Fatalf("entries has %d keys (%v), want %d", len(entries), keysOf(entries), len(want))
	}
	for _, key := range want {
		if _, ok := entries[key]; !ok {
			t.Errorf("entries[%q] is missing", key)
		}
	}
}

// TestCLIEntries proves each CLI entry runs the real command line against the world it
// resolves: the lines, the exit codes and the files it leaves behind. The hub is the one
// server, so every row's world is a temp home whose hub answers — or no home at all.
func TestCLIEntries(t *testing.T) {
	t.Run("help, --help and -h print the usage on stdout and exit 0", func(t *testing.T) {
		for _, command := range []string{"help", "--help", "-h"} {
			isolateState(t)
			var stdout, stderr bytes.Buffer
			if code := Run([]string{command}, &stdout, &stderr, testVersion); code != 0 {
				t.Fatalf("Run(%s) = %d, want 0", command, code)
			}
			want := cliUsageLine + "\nstart names the huddle hub's radar page, starting the hub when the huddle home is set up\n"
			if stdout.String() != want {
				t.Errorf("Run(%s) stdout = %q, want %q", command, stdout.String(), want)
			}
		}
	})

	t.Run("status and url answer the hub page while a hub answers", func(t *testing.T) {
		for _, command := range []string{"status", "url"} {
			isolateState(t)
			hub := healthServer(t, `{"ok":true}`, http.StatusOK)
			setUpHome(t, portOf(t, hub))
			var stdout, stderr bytes.Buffer
			if code := Run([]string{command}, &stdout, &stderr, testVersion); code != 0 {
				t.Fatalf("Run(%s) = %d, stderr %q", command, code, stderr.String())
			}
			want := "radar: in the huddle hub at http://127.0.0.1:" + strconv.Itoa(portOf(t, hub)) + "/#/radar\n"
			if command == "url" {
				want = "radar: http://127.0.0.1:" + strconv.Itoa(portOf(t, hub)) + "/#/radar\n"
			}
			if stdout.String() != want {
				t.Errorf("Run(%s) stdout = %q, want %q", command, stdout.String(), want)
			}
		}
	})

	t.Run("status and url are exit 6 with no hub answering", func(t *testing.T) {
		for _, command := range []string{"status", "url"} {
			isolateState(t)
			setUpHome(t, closedPort(t)) // a home whose port nothing answers
			var stdout, stderr bytes.Buffer
			if code := Run([]string{command}, &stdout, &stderr, testVersion); code != 6 {
				t.Fatalf("Run(%s) = %d, want 6", command, code)
			}
			if stdout.String() != notRunningLine+"\n" {
				t.Errorf("Run(%s) stdout = %q, want the not-running line", command, stdout.String())
			}
		}
	})

	t.Run("stop names the hub and stops nothing", func(t *testing.T) {
		isolateState(t)
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"stop"}, &stdout, &stderr, testVersion); code != 0 {
			t.Fatalf("Run(stop) = %d, stderr %q", code, stderr.String())
		}
		if want := "radar: runs inside the huddle hub; /huddle:stop stops it\n"; stdout.String() != want {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
	})

	t.Run("start with a hub answering names the hub's radar page and starts nothing", func(t *testing.T) {
		state := isolateState(t)
		hub := healthServer(t, `{"ok":true}`, http.StatusOK)
		setUpHome(t, portOf(t, hub))
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"start"}, &stdout, &stderr, testVersion); code != 0 {
			t.Fatalf("Run(start) = %d, stderr %q", code, stderr.String())
		}
		want := "radar: in the huddle hub at http://127.0.0.1:" + strconv.Itoa(portOf(t, hub)) + "/#/radar\n"
		if stdout.String() != want {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
		if _, err := os.Stat(filepath.Join(state, "server.json")); !os.IsNotExist(err) {
			t.Errorf("a standalone dashboard left server.json behind (err %v)", err)
		}
	})

	t.Run("start with no set-up home says to run /huddle:setup and exits 6", func(t *testing.T) {
		isolateState(t)
		t.Setenv("HUDDLE_HOME", filepath.Join(t.TempDir(), "huddle")) // never created
		var stdout, stderr bytes.Buffer
		if code := Run([]string{"start"}, &stdout, &stderr, testVersion); code != 6 {
			t.Fatalf("Run(start) = %d, stdout %q, stderr %q, want 6", code, stdout.String(), stderr.String())
		}
		if want := "radar: the huddle hub is not set up; run /huddle:setup\n"; stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	})
}

// setUpHome lays a huddle home out and records port in its port file, the way a
// started hub leaves it, so the words find the hub.
func setUpHome(t *testing.T, port int) {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv("HUDDLE_HOME", homeDir)
	if err := os.MkdirAll(huddlehome.Home{Root: homeDir}.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(huddlehome.Home{Root: homeDir}.PortFile(), []byte(strconv.Itoa(port)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOpenLaunchesTheOpener proves `open` hands the hub's radar page to the platform's opener
// without a real browser: a fake `open` earlier on PATH records what it got.
func TestOpenLaunchesTheOpener(t *testing.T) {
	isolateState(t)
	hub := healthServer(t, `{"ok":true}`, http.StatusOK)
	setUpHome(t, portOf(t, hub))
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "opened")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" > \"" + marker + "\"\n"
	// macOS runs open, Linux xdg-open: the fake answers to both names.
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatalf("write the fake opener: %v", err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"open"}, &stdout, &stderr, testVersion); code != 0 {
		t.Fatalf("Run(open) = %d, stderr %q", code, stderr.String())
	}
	want := "http://127.0.0.1:" + strconv.Itoa(portOf(t, hub)) + "/#/radar"
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && strings.TrimSuffix(string(data), "\n") == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the opener never got %s (marker holds %q, err %v)", want, data, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRunDispatchesThroughTheTable swaps the table for a recording one-key table, so every key
// is proven to be reached through Run with the arguments after the subcommand name and the
// version the caller passed.
func TestRunDispatchesThroughTheTable(t *testing.T) {
	original := entries
	t.Cleanup(func() { entries = original })
	for _, key := range keysOf(original) {
		var gotCtx context.Context
		var gotArgs []string
		var gotVersion string
		var gotStdout, gotStderr io.Writer
		entries = map[string]runner{
			key: func(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
				gotCtx, gotArgs, gotStdout, gotStderr, gotVersion = ctx, args, stdout, stderr, version
				io.WriteString(stdout, "out\n")
				io.WriteString(stderr, "err\n")
				return 7
			},
		}
		var stdout, stderr bytes.Buffer
		code := Run([]string{key, "--port", "17787"}, &stdout, &stderr, testVersion)
		if code != 7 {
			t.Errorf("Run(%q) = %d, want the entry's 7", []string{key}, code)
		}
		if gotCtx == nil {
			t.Errorf("Run(%q) passed a nil context", []string{key})
		}
		if want := []string{"--port", "17787"}; !reflect.DeepEqual(gotArgs, want) {
			t.Errorf("entry args = %q, want %q", gotArgs, want)
		}
		if gotVersion != testVersion {
			t.Errorf("entry version = %q, want %q", gotVersion, testVersion)
		}
		if gotStdout != io.Writer(&stdout) || gotStderr != io.Writer(&stderr) {
			t.Errorf("Run(%q) did not pass its writers to the entry", []string{key})
		}
		if stdout.String() != "out\n" || stderr.String() != "err\n" {
			t.Errorf("entry output = (%q, %q), want it on Run's writers", stdout.String(), stderr.String())
		}
	}
}

// isolateState points the CLI's state dir at a fresh temp world.
func isolateState(t *testing.T) string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("RADAR_HOME", state)
	return state
}

// healthServer answers every request with one body and status, the way the dashboard answers
// /api/health.
func healthServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

// portOf is one httptest server's loopback port.
func portOf(t *testing.T, server *httptest.Server) int {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatalf("parse %s: %v", server.URL, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse the port of %s: %v", server.URL, err)
	}
	return port
}

// closedPort is a loopback port nothing holds.
func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("draw a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// keysOf returns the table's keys in a stable order.
func keysOf(table map[string]runner) []string {
	keys := make([]string, 0, len(table))
	for key := range table {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
