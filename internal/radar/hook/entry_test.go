// The entry's own cases: the stdin deadline, the autostart decision and the one systemMessage
// it prints, with the dashboard start injected so nothing here spawns a process.
package hook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// entryNow is the fixed clock these cases inject.
var entryNow = time.Date(2026, 1, 15, 6, 30, 0, 0, time.UTC)

// entryEnviron builds a temp HOME/RADAR_HOME world plus the given extra entries.
func entryEnviron(t *testing.T, extra ...string) []string {
	t.Helper()
	home := t.TempDir()
	environ := []string{
		"HOME=" + home,
		"RADAR_HOME=" + filepath.Join(home, "state"),
		"CLAUDE_CONFIG_DIR=" + filepath.Join(home, "claude"),
		"ZAI_STATE_DIR=" + filepath.Join(home, "zai"),
	}
	return append(environ, extra...)
}

// refusingStarter fails the test the moment the entry tries to start a server.
func refusingStarter(t *testing.T) func() bool {
	return func() bool {
		t.Error("the entry started a dashboard it should not have")
		return true
	}
}

func TestRunEntry(t *testing.T) {
	ctx := context.Background()

	t.Run("records the payload and stays silent when autostart is off", func(t *testing.T) {
		environ := entryEnviron(t)
		var stdout bytes.Buffer
		code := runEntry(ctx, strings.NewReader(`{"hook_event_name":"Stop","session_id":"e1"}`),
			&stdout, environ, func() time.Time { return entryNow }, 7, 8, refusingStarter(t))
		if code != 0 {
			t.Fatalf("runEntry = %d, want 0", code)
		}
		if stdout.String() != "" {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
		data, err := os.ReadFile(filepath.Join(radarHome(environ), "spool", "2026-01-15.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"event":"Stop"`) || !strings.Contains(string(data), `"pid":7`) ||
			!strings.Contains(string(data), `"ppid":8`) {
			t.Errorf("spool holds %s, want the Stop line with pid 7 and ppid 8", data)
		}
	})

	t.Run("Main stays silent and exits 0 when autostart is off", func(t *testing.T) {
		environ := entryEnviron(t)
		var stdout bytes.Buffer
		if code := Main(ctx, strings.NewReader(`{}`), &stdout, environ, func() time.Time { return entryNow }, 1, 2); code != 0 {
			t.Fatalf("Main = %d, want 0", code)
		}
		if stdout.String() != "" {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
	})

	t.Run("prints the systemMessage a started dashboard owes the user", func(t *testing.T) {
		environ := entryEnviron(t, "RADAR_AUTOSTART=1")
		state := radarHome(environ)
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(state, "port"), []byte("29871\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		code := runEntry(ctx, strings.NewReader(`{"hook_event_name":"SessionStart"}`),
			&stdout, environ, func() time.Time { return entryNow }, 1, 2, func() bool { return true })
		if code != 0 {
			t.Fatalf("runEntry = %d, want 0", code)
		}
		want := "{\"systemMessage\":\"radar: dashboard at http://127.0.0.1:29871\"}\n"
		if stdout.String() != want {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
	})

	t.Run("starts nothing while a server already runs", func(t *testing.T) {
		environ := entryEnviron(t, "RADAR_AUTOSTART=1")
		state := radarHome(environ)
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		info := "{\"pid\":" + strconv.Itoa(os.Getpid()) + "}\n"
		if err := os.WriteFile(filepath.Join(state, "server.json"), []byte(info), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout bytes.Buffer
		code := runEntry(ctx, strings.NewReader(`{"hook_event_name":"SessionStart"}`),
			&stdout, environ, func() time.Time { return entryNow }, 1, 2, refusingStarter(t))
		if code != 0 {
			t.Fatalf("runEntry = %d, want 0", code)
		}
		if stdout.String() != "" {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
	})

	t.Run("says nothing when the start never launched", func(t *testing.T) {
		environ := entryEnviron(t, "RADAR_AUTOSTART=1")
		var stdout bytes.Buffer
		code := runEntry(ctx, strings.NewReader(`{"hook_event_name":"SessionStart"}`),
			&stdout, environ, func() time.Time { return entryNow }, 1, 2, func() bool { return false })
		if code != 0 {
			t.Fatalf("runEntry = %d, want 0", code)
		}
		if stdout.String() != "" {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
	})

	t.Run("points at the status command when no port is saved yet", func(t *testing.T) {
		environ := entryEnviron(t, "RADAR_AUTOSTART=1")
		var stdout bytes.Buffer
		code := runEntry(ctx, strings.NewReader(`{"hook_event_name":"SessionStart"}`),
			&stdout, environ, func() time.Time { return entryNow }, 1, 2, func() bool { return true })
		if code != 0 {
			t.Fatalf("runEntry = %d, want 0", code)
		}
		want := "{\"systemMessage\":\"radar: starting the dashboard (/huddle:status shows its address)\"}\n"
		if stdout.String() != want {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
	})
}

// radarHome digs RADAR_HOME out of the environment these cases built.
func radarHome(environ []string) string {
	for _, entry := range environ {
		if key, value, _ := strings.Cut(entry, "="); key == "RADAR_HOME" {
			return value
		}
	}
	return ""
}

func TestReadStdin(t *testing.T) {
	t.Run("returns stdin's bytes when they end", func(t *testing.T) {
		if got := readStdin(strings.NewReader("payload")); got != "payload" {
			t.Errorf("readStdin = %q, want payload", got)
		}
	})

	t.Run("gives up on stdin that never closes", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { reader.Close(); writer.Close() })
		started := time.Now()
		if got := readStdin(reader); got != "" {
			t.Errorf("readStdin = %q, want empty", got)
		} else if elapsed := time.Since(started); elapsed < 500*time.Millisecond {
			t.Errorf("readStdin gave up after %v, want the safety deadline", elapsed)
		}
	})
}

func TestServerRunning(t *testing.T) {
	ctx := context.Background()

	t.Run("no server.json is not a running server", func(t *testing.T) {
		if serverRunning(ctx, envOf(entryEnviron(t))) {
			t.Error("a missing server.json counts as running")
		}
	})

	t.Run("an unreadable pid is not a running server", func(t *testing.T) {
		for _, info := range []string{"not json", `{"pid":0}`, `{"pid":-2}`} {
			environ := entryEnviron(t)
			state := radarHome(environ)
			if err := os.MkdirAll(state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "server.json"), []byte(info), 0o600); err != nil {
				t.Fatal(err)
			}
			if serverRunning(ctx, envOf(environ)) {
				t.Errorf("server.json %q counts as running", info)
			}
		}
	})

	t.Run("a live pid is a running server", func(t *testing.T) {
		environ := entryEnviron(t)
		state := radarHome(environ)
		if err := os.MkdirAll(state, 0o700); err != nil {
			t.Fatal(err)
		}
		info := "{\"pid\":" + strconv.Itoa(os.Getpid()) + "}\n"
		if err := os.WriteFile(filepath.Join(state, "server.json"), []byte(info), 0o600); err != nil {
			t.Fatal(err)
		}
		if !serverRunning(ctx, envOf(environ)) {
			t.Error("this test's own pid counts as dead")
		}
	})
}

// The systemMessage line is JSON.stringify's bytes: `<`, `>`, `&` and the two line separators
// raw, so Claude's context sees the message exactly as written.
func TestSystemMessageKeepsJSONStringifyBytes(t *testing.T) {
	want := "{\"systemMessage\":\"a<b>c&d e f\"}"
	if got := systemMessage("a<b>c&d e f"); got != want {
		t.Errorf("systemMessage = %q, want %q", got, want)
	}
}
