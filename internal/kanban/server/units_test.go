package server

// The small decisions on their own: the file types the board's page is served
// as, the symlinks a board path may carry, and the page stream that drops a
// slow reader instead of blocking the publisher.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContentTypeOfNamesEveryFileTheBoardServes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want string
	}{
		{"index.html", "text/html; charset=utf-8"},
		{"page.htm", "text/html; charset=utf-8"},
		{"app.js", "text/javascript; charset=utf-8"},
		{"app.mjs", "text/javascript; charset=utf-8"},
		{"board.css", "text/css; charset=utf-8"},
		{"logo.svg", "image/svg+xml"},
		{"logo.png", "image/png"},
		{"data.bin", "application/octet-stream"},
		{"no-extension", "application/octet-stream"},
	}
	for _, tt := range tests {
		if got := contentTypeOf(tt.name); got != tt.want {
			t.Errorf("contentTypeOf(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestResolveSymlinksKeepsAGoneTailOnItsLivingAncestor(t *testing.T) {
	t.Parallel()
	living := t.TempDir()
	board := filepath.Join(living, "agents", "kanban")
	if err := os.MkdirAll(board, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "board")
	if err := os.Symlink(board, linked); err != nil {
		t.Fatal(err)
	}

	t.Run("a path that resolves stays resolved", func(t *testing.T) {
		resolved, err := filepath.EvalSymlinks(board)
		if err != nil {
			t.Fatal(err)
		}
		if got := resolveSymlinks(board); got != resolved {
			t.Errorf("resolveSymlinks = %q, want %q", got, resolved)
		}
	})

	t.Run("a gone tail keeps its spelling on the resolved ancestor", func(t *testing.T) {
		resolved, err := filepath.EvalSymlinks(board)
		if err != nil {
			t.Fatal(err)
		}
		gone := filepath.Join(linked, "ready", "HR-1.md")
		want := filepath.Join(resolved, "ready", "HR-1.md")
		if got := resolveSymlinks(gone); got != want {
			t.Errorf("resolveSymlinks = %q, want %q", got, want)
		}
	})

	t.Run("the root is itself", func(t *testing.T) {
		if got := resolveSymlinks("/"); got != "/" {
			t.Errorf("resolveSymlinks(/) = %q, want / unchanged", got)
		}
	})

	t.Run("a relative path is its own ancestor", func(t *testing.T) {
		got := resolveSymlinks("gone-board")
		if strings.HasPrefix(got, "/") {
			t.Errorf("resolveSymlinks = %q, want the relative spelling kept", got)
		}
	})
}

func TestEnqueueDropsASlowReaderInsteadOfBlocking(t *testing.T) {
	t.Parallel()
	t.Run("a dead client takes no frames", func(t *testing.T) {
		client := newClient()
		client.drop()
		client.enqueue(sseFrame{name: "board", data: "{}"})
		select {
		case <-client.frames:
			t.Error("a dead client kept a frame")
		default:
		}
	})

	t.Run("a full queue drops the page", func(t *testing.T) {
		client := newClient()
		for i := 0; i < 64; i++ {
			client.enqueue(sseFrame{name: "board", data: "{}"})
		}
		client.enqueue(sseFrame{name: "board", data: "{}"})
		select {
		case <-client.dead:
		default:
			t.Error("a full queue left the page alive")
		}
	})
}
