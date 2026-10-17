// Package kanbantab serves the boards of the sessions huddle knows about under
// /api/kanban: the read routes the kanban page loads with, answered in-process
// by each board's own app with no port of its own, and the flow commands that
// write a board's cards. Huddle's credential guards the prefix; the board apps
// run without the Host guard the standalone server answers with, because the
// server that mounts them owns host and origin.
package kanbantab

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// Prefix is the huddle path the boards live under: /api/kanban/boards names
// them, /api/kanban/b/<key>/api/… answers one board's API.
const Prefix = "/api/kanban"

// Options shapes the mount. Roots answers the working dirs to look for boards
// from — huddle hands it the registry's sessions' cwds — and Version and Now
// travel into every board app. Audit, when set, sees one call per write the
// flow route runs, ok naming whether the board took it.
type Options struct {
	Roots   func(ctx context.Context) []string
	Version string
	Now     func() time.Time
	Audit   func(ctx context.Context, r *http.Request, command, key, id string, ok bool)
}

// entry is one mounted board: its app and the watch run under the cancel.
type entry struct {
	app    *server.App
	cancel context.CancelFunc
}

// Tab is every board a Roots pass has answered through the prefix, each held
// with its watch running until Stop.
type Tab struct {
	ctx     context.Context
	roots   func(ctx context.Context) []string
	version string
	now     func() time.Time
	audit   func(ctx context.Context, r *http.Request, command, key, id string, ok bool)

	mu     sync.Mutex
	apps   map[string]*entry
	flowMu sync.Mutex
}

// Mount builds the tab. Serving starts when the caller serves its handler;
// a board's watch starts on that board's first request under the prefix, and
// Stop ends them all.
func Mount(ctx context.Context, opts Options) *Tab {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Tab{
		ctx:     ctx,
		roots:   opts.Roots,
		version: opts.Version,
		now:     opts.Now,
		audit:   opts.Audit,
		apps:    map[string]*entry{},
	}
}

// boardRow is one board in the /boards answer: its key, its root and the repo
// dir it sits in.
type boardRow struct {
	Key  string `json:"key"`
	Root string `json:"root"`
	Name string `json:"name"`
}

// boardsAnswer is the /boards body; the list is never null.
type boardsAnswer struct {
	Boards []boardRow `json:"boards"`
}

// errorAnswer is one refusal, the shape kanban's own answers take.
type errorAnswer struct {
	Error string `json:"error"`
}

// ServeHTTP answers one request under the prefix: the board list, or one
// board's API with the board part of the path stripped off.
func (t *Tab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path != Prefix && !strings.HasPrefix(path, Prefix+"/") {
		writeJSON(w, http.StatusNotFound, errorAnswer{Error: "not found: " + path})
		return
	}
	switch rest := strings.TrimPrefix(path, Prefix); {
	case rest == "/boards":
		t.oneMethod(w, r, http.MethodGet, func() {
			writeJSON(w, http.StatusOK, boardsAnswer{Boards: boardsOf(t.roots(r.Context()))})
		})
	case strings.HasPrefix(rest, "/b/"):
		t.serveBoard(w, r, strings.TrimPrefix(rest, "/b/"))
	default:
		writeJSON(w, http.StatusNotFound, errorAnswer{Error: "not found: " + path})
	}
}

// serveBoard hands one request to its board's app: the key decides the board,
// the rest of the path is the app's own to answer. The slash after the key is
// the cut's separator, so the app's path gains its own leading slash back.
func (t *Tab) serveBoard(w http.ResponseWriter, r *http.Request, tail string) {
	key, rest, _ := strings.Cut(tail, "/")
	if key == "" {
		writeJSON(w, http.StatusNotFound, errorAnswer{Error: "not found: " + r.URL.Path})
		return
	}
	if rest == "flow" {
		t.flowRoute(w, r, key)
		return
	}
	app := t.appFor(r.Context(), key)
	if app == nil {
		writeJSON(w, http.StatusNotFound, errorAnswer{Error: "no board " + key})
		return
	}
	forwarded := r.Clone(r.Context())
	forwarded.URL.Path = "/" + rest
	forwarded.URL.RawPath = ""
	app.API().ServeHTTP(w, forwarded)
}

// appFor answers the board's app, starting its watch on first use; nil when no
// board answers the key. A key the last Roots pass missed is resolved again
// once, so a board created after the mount answers without a restart.
func (t *Tab) appFor(ctx context.Context, key string) *server.App {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.apps[key]; ok {
		return e.app
	}
	for _, row := range boardsOf(t.roots(ctx)) {
		if row.Key != key {
			continue
		}
		app := server.NewApp(server.Options{Board: board.Board{Root: row.Root}, Version: t.version, Now: t.now})
		runCtx, cancel := context.WithCancel(t.ctx)
		app.Run(runCtx)
		e := &entry{app: app, cancel: cancel}
		t.apps[key] = e
		return e.app
	}
	return nil
}

// Stop ends every mounted board's watch and closes its event streams. A board
// asked for later mounts again fresh. Idempotent.
func (t *Tab) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, e := range t.apps {
		e.cancel()
		delete(t.apps, key)
	}
}

// boardsOf resolves every working dir to its board, folding the dirs of one
// repo into one row: the walk stops at the repo root, so every cwd in a repo
// names the same board. The list is deduped by key and sorted by name, then
// key.
func boardsOf(roots []string) []boardRow {
	seen := map[string]bool{}
	out := []boardRow{}
	for _, cwd := range roots {
		if cwd == "" {
			continue
		}
		b, err := board.Resolve("", noEnv, cwd)
		if err != nil {
			continue // a dir with no board is no row, never an error
		}
		key := server.KeyOf(b.Root)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, boardRow{Key: key, Root: absRoot(b.Root), Name: repoName(b.Root)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// noEnv answers nothing found, so the huddle process's own KANBAN_BOARD never
// picks a board for a session's working dir.
func noEnv(string) (string, bool) { return "", false }

// absRoot answers the board root absolute and clean — the spelling the
// watcher stamps into every snapshot's board field, so a listed root matches
// the answer its board's API gives.
func absRoot(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	return abs
}

// repoName answers the dir a board sits in: the repo, the parent of .agents.
func repoName(root string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(root)))
}

// oneMethod answers one route for its one method, 405 for the others.
func (t *Tab) oneMethod(w http.ResponseWriter, r *http.Request, method string, serve func()) {
	if r.Method != method {
		writeJSON(w, http.StatusMethodNotAllowed, errorAnswer{Error: "method not allowed"})
		return
	}
	serve()
}

// writeJSON writes one JSON answer: the body one line, the headers kanban's
// own answers carry.
func writeJSON(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		encoded = []byte("null")
	}
	encoded = append(encoded, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(encoded)))
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
