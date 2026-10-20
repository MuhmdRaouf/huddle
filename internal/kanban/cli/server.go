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

// The server commands: start, stop, open, refresh and serve. The huddle hub
// serves every board in process, so start and open only name its page — and
// start the hub through EnsureHub when the huddle home is set up and nothing
// answers yet. stop is words: /huddle:stop is what ends the hub. refresh asks
// the hub when one answers and else a server someone runs by hand; serve stays
// that hand-run foreground server, and nothing spawns it.

// ensureBudgetMs is what start and open grant EnsureHub: the hub's own start
// runs inside it, detached, and the answer comes back within it too.
const ensureBudgetMs = 35_000

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
	if w.OpenBrowser != nil {
		fx.OpenBrowser = w.OpenBrowser
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
	// open name it and start no server, so no board holds a port of its own.
	key := board.RepoKey(board.OriginOf(ctx, board.RepoRootOf(abs)), server.KeyOf(abs))
	hub := func() (string, bool) {
		if w.HubBoard == nil {
			return "", false
		}
		return w.HubBoard(ctx, key)
	}
	// ensure is the one way a word without an answer starts the hub; its failure
	// is the word's answer too.
	ensure := func() (string, bool) {
		if w.EnsureHub == nil {
			return "", false
		}
		base, ok, msg := w.EnsureHub(ctx, ensureBudgetMs)
		if !ok {
			fmt.Fprintln(stderr, "kanban: "+msg)
			return "", false
		}
		return base + "/#/kanban/" + key, true
	}
	switch command {
	case "start", "open":
		// --port is kept for older scripts; it names no server any more, but a
		// value that is no port stays a usage error.
		if _, bad := portFlag(v); bad != "" {
			usageError(stderr, commands[command], bad)
			return 2
		}
		link, ok := hub()
		if !ok {
			if link, ok = ensure(); !ok {
				return 6
			}
		}
		fmt.Fprintf(stdout, "kanban: board at %s (%s)\n", link, abs)
		if mayOpen() {
			fx.OpenBrowser(link)
		}
		return 0
	case "stop":
		fmt.Fprintln(stdout, "kanban: boards are served by the huddle hub; /huddle:stop stops it")
		return 0
	case "refresh":
		if _, ok := hub(); ok {
			fmt.Fprintln(stdout, "kanban: the hub watches the board; nothing to refresh")
			return 0
		}
		rev, err := server.Refresh(ctx, abs, home, fx)
		if err != nil {
			fmt.Fprintf(stderr, "kanban: no server for %s; run kanban serve\n", abs)
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
