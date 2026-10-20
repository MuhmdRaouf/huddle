package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// The server commands: start, stop, open, refresh and serve. The board is
// still the subject — a missing board is exit 4 — but the effects reach a
// server, not the files: start spawns one detached and follows its state file,
// stop and refresh talk to the running one, and serve is the foreground server
// kanban start runs.

// runServerCommand runs one server command; v is the parsed flags.
func runServerCommand(ctx context.Context, w World, command string, v *flagValues, boardPath string, stdout, stderr io.Writer) int {
	b, closeBoard, err := boardAt(ctx, w, boardPath)
	if err != nil {
		fmt.Fprintln(stderr, noBoardLine)
		return 4
	}
	defer closeBoard()
	abs := b.Root
	if resolved, err := filepath.Abs(b.Root); err == nil {
		abs = resolved
	}
	fx := server.DefaultEffects()
	if w.ServerFetch != nil {
		fx.Fetch = w.ServerFetch
	}
	if w.SpawnServe != nil {
		fx.SpawnServe = w.SpawnServe
	}
	if w.OpenBrowser != nil {
		fx.OpenBrowser = w.OpenBrowser
	}
	if w.Sleep != nil {
		fx.Sleep = w.Sleep
	}
	home := server.StateDir(w.Getenv)
	mayOpen := func() bool {
		if v != nil && v.boolean("no-open") {
			return false
		}
		if value, ok := w.Getenv("KANBAN_NO_OPEN"); ok && value == "1" {
			return false
		}
		return true
	}
	// the hub's page is the board's address wherever a hub answers: start and
	// open name it and start nothing, so no board holds a port of its own. A
	// machine with no hub still serves one board itself.
	hub := func() (string, bool) {
		if w.HubBoard == nil {
			return "", false
		}
		key := board.RepoKey(board.OriginOf(ctx, board.RepoRootOf(abs)), server.KeyOf(abs))
		return w.HubBoard(ctx, key)
	}
	switch command {
	case "start":
		if link, ok := hub(); ok {
			fmt.Fprintf(stdout, "kanban: board at %s (%s)\n", link, abs)
			if mayOpen() {
				fx.OpenBrowser(link)
			}
			return 0
		}
		port, bad := portFlag(v)
		if bad != "" {
			usageError(stderr, commands["start"], bad)
			return 2
		}
		url, err := server.Start(ctx, abs, home, port, fx)
		if err != nil {
			fmt.Fprintf(stderr, "kanban: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "kanban: board at %s (%s)\n", url, abs)
		if mayOpen() {
			fx.OpenBrowser(url)
		}
		return 0
	case "stop":
		if err := server.Stop(ctx, abs, home, fx); err != nil {
			fmt.Fprintf(stderr, "kanban: %v\n", err)
			return 6
		}
		fmt.Fprintln(stdout, "kanban: stopped")
		return 0
	case "open":
		if link, ok := hub(); ok {
			fmt.Fprintf(stdout, "kanban: board at %s (%s)\n", link, abs)
			if mayOpen() {
				fx.OpenBrowser(link)
			}
			return 0
		}
		recorded := server.Looking(ctx, abs, home, fx)
		if recorded == nil {
			fmt.Fprintf(stderr, "kanban: no server for %s; run kanban start\n", abs)
			return 6
		}
		url := fmt.Sprintf("http://127.0.0.1:%d", recorded.Port)
		fmt.Fprintf(stdout, "kanban: board at %s (%s)\n", url, abs)
		if mayOpen() {
			fx.OpenBrowser(url)
		}
		return 0
	case "refresh":
		rev, err := server.Refresh(ctx, abs, home, fx)
		if err != nil {
			fmt.Fprintf(stderr, "kanban: no server for %s; run kanban start\n", abs)
			return 6
		}
		fmt.Fprintf(stdout, "kanban: refreshed (rev %d)\n", rev)
		return 0
	default: // serve
		port, bad := portFlag(v)
		if bad != "" {
			usageError(stderr, commands["serve"], bad)
			return 2
		}
		_, err := server.Serve(ctx, server.Options{
			Board: b, Home: home, Version: w.Version, Port: port, Stdout: stdout,
		})
		if err != nil {
			fmt.Fprintf(stderr, "kanban: %v\n", err)
			return 1
		}
		return 0
	}
}

// runExport writes the board as one self-contained page. --out names it; its
// directory must already exist (a usage error names it otherwise), and the
// board is read from its files — no server needs to run.
func runExport(ctx context.Context, b board.Board, out string, now func() time.Time, stdout, stderr io.Writer) int {
	dir := filepath.Dir(out)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		usageError(stderr, commands["export"], "the directory "+dir+" does not exist; create it first")
		return 2
	}
	page, count, err := server.ExportPage(ctx, b, now)
	if err != nil {
		fmt.Fprintf(stderr, "kanban: %v\n", err)
		return 1
	}
	if err := os.WriteFile(out, page, 0o644); err != nil {
		fmt.Fprintf(stderr, "kanban: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "kanban: wrote %s (%d tickets)\n", out, count)
	return 0
}

// portFlag reads one --port value: absent takes the default walk (18800 up to
// 18849), a number pins it, and 0 lets the OS pick at once (carried as -1, the
// value no flag parse would produce).
func portFlag(v *flagValues) (int, string) {
	raw := ""
	if v != nil {
		raw = v.str("port")
	}
	if raw == "" {
		return 0, ""
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 0 || port > 65535 {
		return 0, "--port needs a number in 0..65535, got: " + raw
	}
	if port == 0 {
		return -1, ""
	}
	return port, ""
}
