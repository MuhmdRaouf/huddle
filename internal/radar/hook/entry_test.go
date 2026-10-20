// The entry's own cases: the stdin deadline and the one systemMessage it
// prints. The hook starts no server of its own — with a hub it names the hub's
// radar page, with none it answers nothing — so nothing here spawns a process.
package hook

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
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

// noHub is the hub lookup for the cases that run with no hub on the machine.
func noHub(context.Context) (string, bool) { return "", false }

func TestRunEntry(t *testing.T) {
	ctx := context.Background()

	t.Run("records the payload and stays silent outside SessionStart", func(t *testing.T) {
		environ := entryEnviron(t)
		var stdout bytes.Buffer
		code := runEntry(ctx, strings.NewReader(`{"hook_event_name":"Stop","session_id":"e1"}`),
			&stdout, environ, func() time.Time { return entryNow }, 7, 8, noHub)
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

	t.Run("Main stays silent and exits 0 with no hub and autostart off", func(t *testing.T) {
		environ := entryEnviron(t)
		var stdout bytes.Buffer
		if code := Main(ctx, strings.NewReader(`{}`), &stdout, environ, func() time.Time { return entryNow }, 1, 2); code != 0 {
			t.Fatalf("Main = %d, want 0", code)
		}
		if stdout.String() != "" {
			t.Errorf("stdout = %q, want empty", stdout.String())
		}
	})

	t.Run("SessionStart with no hub answers nothing, autostart on or not", func(t *testing.T) {
		for _, extra := range []string{"RADAR_AUTOSTART=1", ""} {
			environ := entryEnviron(t, extra)
			// even a dashboard of an older edition recorded as running starts nothing
			state := radarHome(environ)
			if err := os.MkdirAll(state, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "server.json"), []byte(`{"pid":1}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(state, "port"), []byte("29871\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			code := runEntry(ctx, strings.NewReader(`{"hook_event_name":"SessionStart"}`),
				&stdout, environ, func() time.Time { return entryNow }, 1, 2, noHub)
			if code != 0 {
				t.Fatalf("runEntry(%q) = %d, want 0", extra, code)
			}
			if stdout.String() != "" {
				t.Errorf("stdout = %q, want empty: with no hub the hook starts and says nothing", stdout.String())
			}
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

// The systemMessage line is JSON.stringify's bytes: `<`, `>`, `&` and the two line separators
// raw, so Claude's context sees the message exactly as written.
func TestSystemMessageKeepsJSONStringifyBytes(t *testing.T) {
	want := "{\"systemMessage\":\"a<b>c&d e f\"}"
	if got := systemMessage("a<b>c&d e f"); got != want {
		t.Errorf("systemMessage = %q, want %q", got, want)
	}
}
