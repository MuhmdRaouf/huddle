// Package kanbantab serves the boards of the sessions huddle knows about under
// /api/kanban: the read routes the kanban page loads with, answered in-process
// by each board's own app with no port of its own — or streamed from the
// huddle that hosts the board, when a ReadRelay carries them there — and the
// flow commands that write a board's cards. Huddle's credential guards the
// prefix; the board apps run without the Host guard the standalone server
// answers with, because the server that mounts them owns host and origin.
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
// from — huddle hands it the registry's sessions' cwds — Origin answers a
// repository's origin URL (the default reads git's config, bounded at a
// second), and Version and Now travel into every board app. Audit, when set,
// sees one call per write the flow and feature routes run, ok naming whether
// the board took it. Owner, when set, answers whether one request is the
// owner's; the feature route takes the owner's writes alone, and a nil Owner
// answers false for everyone. Relay, when set, carries one flow command for a
// board another huddle hosts to that huddle before the local board is read;
// relayed names a verdict to write through unchanged, false leaves the command
// local. ReadRelay, when set, carries one GET read under a board's api/ to
// that huddle first; relayed names a response to stream through, false leaves
// the read on the local app.
type Options struct {
	Roots     func(ctx context.Context) []string
	Origin    func(ctx context.Context, repoRoot string) string
	Version   string
	Now       func() time.Time
	Audit     func(ctx context.Context, r *http.Request, command, key, id string, ok bool)
	Owner     func(r *http.Request) bool
	Relay     func(ctx context.Context, key string, body []byte) (status int, answer []byte, relayed bool)
	ReadRelay func(ctx context.Context, key, path string) (response *http.Response, relayed bool)
}

// entry is one mounted board: its app, the watch run under the cancel, and the
// close that ends the board's own store binding with them.
type entry struct {
	app    *server.App
	cancel context.CancelFunc
	close  func() error
}

// Tab is every board a Roots pass has answered through the prefix, each held
// with its watch running until Stop.
type Tab struct {
	ctx       context.Context
	roots     func(ctx context.Context) []string
	originOf  func(ctx context.Context, repoRoot string) string
	version   string
	now       func() time.Time
	audit     func(ctx context.Context, r *http.Request, command, key, id string, ok bool)
	owner     func(r *http.Request) bool
	relay     func(ctx context.Context, key string, body []byte) (int, []byte, bool)
	readRelay func(ctx context.Context, key, path string) (*http.Response, bool)

	mu       sync.Mutex
	apps     map[string]*entry
	flowMu   sync.Mutex
	originMu sync.Mutex
	origins  map[string]string
}

// Mount builds the tab. Serving starts when the caller serves its handler;
// a board's watch starts on that board's first request under the prefix, and
// Stop ends them all.
func Mount(ctx context.Context, opts Options) *Tab {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Origin == nil {
		opts.Origin = board.OriginOf
	}
	if opts.Owner == nil {
		// a tab mounted without the answer refuses every feature write
		opts.Owner = func(*http.Request) bool { return false }
	}
	return &Tab{
		ctx:       ctx,
		roots:     opts.Roots,
		originOf:  opts.Origin,
		version:   opts.Version,
		now:       opts.Now,
		audit:     opts.Audit,
		owner:     opts.Owner,
		relay:     opts.Relay,
		readRelay: opts.ReadRelay,
		apps:      map[string]*entry{},
		origins:   map[string]string{},
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
			writeJSON(w, http.StatusOK, boardsAnswer{Boards: t.boardsOf(r.Context())})
		})
	case strings.HasPrefix(rest, "/b/"):
		t.serveBoard(w, r, strings.TrimPrefix(rest, "/b/"))
	default:
		writeJSON(w, http.StatusNotFound, errorAnswer{Error: "not found: " + path})
	}
}

// serveBoard hands one request to its board's app: the key decides the board,
// the rest of the path is the app's own to answer. The slash after the key is
// the cut's separator, so the app's path gains its own leading slash back. A
// GET read under the board's api/ asks the ReadRelay first: a board another
// huddle hosts is that huddle's to answer, its event stream included, and
// anything the relay does not take serves from the local app.
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
	if rest == "feature" {
		t.featureRoute(w, r, key)
		return
	}
	if t.readRelay != nil && relayedRead(r.Method, rest) {
		if response, relayed := t.readRelay(r.Context(), key, strings.TrimPrefix(rest, "api/")); relayed {
			writeRelayedRead(w, response)
			return
		}
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

// relayedRead answers whether one board path is a read another huddle may
// answer: a GET under the board's own api/, never the control routes — refresh
// and shutdown are the standalone server's, and a satellite pulls nothing of
// them.
func relayedRead(method, rest string) bool {
	return method == http.MethodGet && strings.HasPrefix(rest, "api/") &&
		rest != "api/refresh" && rest != "api/shutdown"
}

// writeRelayedRead streams one relayed read through: the hub's status, its
// answer headers, then its body with a flush after every write, so the event
// stream reaches the page as the hub sends it. The stream ends when the client
// or the hub does — the read runs on the request's context, and the hub's body
// ends with it.
func writeRelayedRead(w http.ResponseWriter, response *http.Response) {
	defer func() { _ = response.Body.Close() }()
	header := w.Header()
	if ct := response.Header.Get("Content-Type"); ct != "" {
		header.Set("Content-Type", ct)
	}
	if cc := response.Header.Get("Cache-Control"); cc != "" {
		header.Set("Cache-Control", cc)
	}
	w.WriteHeader(response.StatusCode)
	flusher, _ := w.(http.Flusher)
	buffer := make([]byte, 32*1024)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			if _, werr := w.Write(buffer[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// appFor answers the board's app, starting its watch on first use; nil when no
// board answers the key. A key the last Roots pass missed is resolved again
// once, so a board created after the mount answers without a restart. The board
// opens the way the cli opens it — a root holding kanban.db binds its store, so
// the watch and the reads see its rows, not the folder the migration archived
// away.
func (t *Tab) appFor(ctx context.Context, key string) *server.App {
	t.mu.Lock()
	defer t.mu.Unlock()
	if e, ok := t.apps[key]; ok {
		return e.app
	}
	for _, row := range t.boardsOf(ctx) {
		if row.Key != key {
			continue
		}
		b, closeBoard, err := board.Open(ctx, board.Board{Root: row.Root, Now: t.now})
		if err != nil {
			return nil
		}
		app := server.NewApp(server.Options{Board: b, Version: t.version, Now: t.now})
		runCtx, cancel := context.WithCancel(t.ctx)
		app.Run(runCtx)
		e := &entry{app: app, cancel: cancel, close: closeBoard}
		t.apps[key] = e
		return e.app
	}
	return nil
}

// Stop ends every mounted board's watch, closes its event streams and releases
// its store binding. A board asked for later mounts again fresh. Idempotent.
func (t *Tab) Stop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, e := range t.apps {
		e.cancel()
		_ = e.close()
		delete(t.apps, key)
	}
}

// boardsOf resolves every working dir to its board, folding the dirs of one
// repo into one row: the walk stops at the repo root, so every cwd in a repo
// names the same board. Each row's key is BoardKey's answer for the board's
// root, and the list is deduped by key and sorted by name, then key.
func (t *Tab) boardsOf(ctx context.Context) []boardRow {
	seen := map[string]bool{}
	out := []boardRow{}
	for _, cwd := range t.roots(ctx) {
		if cwd == "" {
			continue
		}
		b, err := board.Resolve("", noEnv, cwd)
		if err != nil {
			continue // a dir with no board is no row, never an error
		}
		key := t.BoardKey(ctx, b.Root)
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

// BoardKey answers the key the boards list gives the board rooted at root —
// the repository's identity: the normalised origin, so clones of one
// repository share their board, and a repo with no origin keeps the key its
// path decides. boardsOf answers every row's key through this one method, so
// a caller outside the tab names a board the way the list does.
func (t *Tab) BoardKey(ctx context.Context, root string) string {
	return board.RepoKey(t.origin(ctx, board.RepoRootOf(root)), server.KeyOf(root))
}

// origin answers the repo root's origin URL through the tab's Origin, keeping
// the last non-empty answer per root: the lookup is a subprocess bounded at a
// second that answers "" on a timeout, and a board's key must not change with
// one slow git. A root never seen with an origin stays empty, so the path key
// still decides.
func (t *Tab) origin(ctx context.Context, repoRoot string) string {
	if o := t.originOf(ctx, repoRoot); o != "" {
		t.originMu.Lock()
		t.origins[repoRoot] = o
		t.originMu.Unlock()
		return o
	}
	t.originMu.Lock()
	defer t.originMu.Unlock()
	return t.origins[repoRoot]
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
	return filepath.Base(board.RepoRootOf(root))
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
