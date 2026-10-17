package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The state files: what writes, what reads back, and what never answers a
// server.

func TestStateRoundTrip(t *testing.T) {
	home := t.TempDir()
	key := KeyOf("/a/board")
	written := State{
		Pid: 42, Port: 18800, Board: "/a/board", Version: "9.9",
		Token: "abc", StartedAt: "2026-10-15T12:00:00+03:00",
	}
	if err := WriteState(context.Background(), home, key, written); err != nil {
		t.Fatal(err)
	}
	read := ReadState(home, key)
	if read == nil {
		t.Fatal("the state did not read back")
	}
	if *read != written {
		t.Errorf("read = %+v, want %+v", *read, written)
	}

	t.Run("the file is one line and private", func(t *testing.T) {
		data, err := os.ReadFile(StatePath(home, key))
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n"); len(lines) != 1 {
			t.Errorf("the state holds %d lines, want 1", len(lines))
		}
		if !strings.HasPrefix(string(data), `{"pid":42,"port":18800,"board":"/a/board"`) {
			t.Errorf("state = %q, want the pid, port and board first", data)
		}
	})

	t.Run("remove unlinks it", func(t *testing.T) {
		RemoveState(home, key)
		if ReadState(home, key) != nil {
			t.Error("the state survived its removal")
		}
		RemoveState(home, key) // twice is fine
	})
}

func TestReadStateRefusesBrokenRecords(t *testing.T) {
	home := t.TempDir()
	key := "board00000001"

	t.Run("missing", func(t *testing.T) {
		if ReadState(home, key) != nil {
			t.Error("a missing file answered a server")
		}
	})

	t.Run("not json", func(t *testing.T) {
		if err := os.WriteFile(StatePath(home, key), []byte("not json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if ReadState(home, key) != nil {
			t.Error("garbage answered a server")
		}
	})

	t.Run("without a port or a board", func(t *testing.T) {
		for _, body := range []string{`{"pid":1}`, `{"port":18800}`, `{"port":0,"board":"/b"}`} {
			if err := os.WriteFile(StatePath(home, key), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if ReadState(home, key) != nil {
				t.Errorf("%s answered a server", body)
			}
		}
	})
}

func TestAppendLog(t *testing.T) {
	t.Run("each line lands, newest last", func(t *testing.T) {
		home := t.TempDir()
		key := "board00000001"
		AppendLog(home, key, "one")
		AppendLog(home, key, "two")
		data, err := os.ReadFile(LogPath(home, key))
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "one\ntwo\n" {
			t.Errorf("log = %q", data)
		}
	})

	t.Run("no home and an unwritable home are silent", func(t *testing.T) {
		AppendLog("", "k", "line") // never panics
		blocked := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		AppendLog(blocked, "k", "line") // a file where the dir should be
	})
}

func TestStateDirBranches(t *testing.T) {
	t.Run("an empty KANBAN_HOME falls through to the home", func(t *testing.T) {
		env := map[string]string{"KANBAN_HOME": "", "HOME": "/home/tester"}
		got := StateDir(func(name string) (string, bool) { v, ok := env[name]; return v, ok })
		if want := filepath.Join(".agents", "kanban-server"); !strings.HasSuffix(got, want) {
			t.Errorf("StateDir = %q, want it under %q", got, want)
		}
	})

	t.Run("an unset home still answers a directory", func(t *testing.T) {
		if got := StateDir(func(string) (string, bool) { return "", false }); got == "" {
			t.Error("StateDir answered nothing")
		}
	})
}

func TestAbsPathKeepsRelativePathsWorking(t *testing.T) {
	if got := absPath("relative/board"); !filepath.IsAbs(got) {
		t.Errorf("absPath = %q, want an absolute path", got)
	}
}

// TestKeyOfAnswersOneKeyForEverySpelling: the hook resolves a board from
// CLAUDE_PROJECT_DIR and the commands from the working directory, and the two
// spellings differ on any symlinked path, so the key — and with it the one
// server a board has — must follow the resolution, not the spelling.
func TestKeyOfAnswersOneKeyForEverySpelling(t *testing.T) {
	board := t.TempDir()
	linked := filepath.Join(t.TempDir(), "board")
	if err := os.Symlink(board, linked); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(board, "agents", "kanban")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	spellings := []string{
		board,
		linked,
		filepath.Join(board, ".", "agents", "..", "."),
		filepath.Join(linked, "agents", ".."),
	}
	for _, spelling := range spellings {
		if KeyOf(spelling) != KeyOf(board) {
			t.Errorf("KeyOf(%q) = %q, want the key of %q (%q)", spelling, KeyOf(spelling), board, KeyOf(board))
		}
	}
}

// TestStateStartedAtShape pins the startedAt stamp's shape through one serve.
func TestStateStartedAtShape(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	serveInProcess(t, Options{
		Board: b, Home: home, Version: "test", Port: -1, EveryMs: 25,
		Now: func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC) },
	})
	st := waitState(t, home, b.Root)
	if st.StartedAt != "2026-10-15T12:00:00Z" {
		t.Errorf("startedAt = %q, want the RFC 3339 stamp", st.StartedAt)
	}
}
