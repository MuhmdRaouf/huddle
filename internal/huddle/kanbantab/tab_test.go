package kanbantab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// The tab as huddle mounts it: the boards of the registry's working dirs listed
// under /boards, each board's read API answered through /b/<key> by the same
// app the standalone server would run, with no port of its own, and Stop
// ending every board's watch.

// pinnedNow is the clock every board in these tests runs on, so two snapshots
// of one board are the same bytes.
func pinnedNow() time.Time {
	return time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
}

// newRepo answers one repo holding a board: .git ends the cwd walk upward, the
// board sits at .agents/kanban.
func newRepo(t *testing.T, home, name string) string {
	t.Helper()
	repo := filepath.Join(home, name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := board.Init(context.Background(), boardRoot(repo), "", pinnedNow()); err != nil {
		t.Fatal(err)
	}
	return repo
}

// boardRoot answers the repo's board root.
func boardRoot(repo string) string {
	return filepath.Join(repo, ".agents", "kanban")
}

// writeTicket lands one minimal ticket file in a column.
func writeTicket(t *testing.T, repo, col, id string) {
	t.Helper()
	text := "---\nid: " + id + "\ntitle: \"" + id + " title\"\ntype: docs\ncluster: kanban\n" +
		"summary: \"one\"\n---\n\n# " + id + "\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(boardRoot(repo), col, id+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// answer runs one request through a handler and answers status and body.
func answer(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// staticRoots answers one Roots that always returns the same working dirs.
func staticRoots(dirs ...string) func(context.Context) []string {
	return func(context.Context) []string { return dirs }
}

// resolvedRoot answers the board root the way the tab spells it: absolute and
// clean, the spelling the snapshot's board field carries.
func resolvedRoot(t *testing.T, root string) string {
	t.Helper()
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func TestBoardsListsEveryRepoOnce(t *testing.T) {
	home := t.TempDir()
	repoA := newRepo(t, home, "alpha")
	repoB := newRepo(t, home, "beta")
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(filepath.Join(repoA, "inner", "deep"), filepath.Join(repoA, "other"), repoB),
	})
	status, body := answer(t, tab, http.MethodGet, Prefix+"/boards")
	if status != http.StatusOK {
		t.Fatalf("/boards = %d %s, want 200", status, body)
	}
	var listed struct {
		Boards []struct {
			Key  string `json:"key"`
			Root string `json:"root"`
			Name string `json:"name"`
		} `json:"boards"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if len(listed.Boards) != 2 {
		t.Fatalf("boards = %s, want one per repo: two cwds of repo A fold into one board", body)
	}
	want := []struct{ key, root, name string }{
		{server.KeyOf(boardRoot(repoA)), resolvedRoot(t, boardRoot(repoA)), "alpha"},
		{server.KeyOf(boardRoot(repoB)), resolvedRoot(t, boardRoot(repoB)), "beta"},
	}
	for i, w := range want {
		got := listed.Boards[i]
		if got.Key != w.key || got.Name != w.name || got.Root != w.root {
			t.Errorf("board %d = %s %s %s, want %s %s %s", i, got.Key, got.Root, got.Name, w.key, w.root, w.name)
		}
	}
}

func TestBoardsIgnoresDirsWithNoBoard(t *testing.T) {
	home := t.TempDir()
	plain := filepath.Join(home, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	tab := Mount(context.Background(), Options{Roots: staticRoots(plain, "")})
	status, body := answer(t, tab, http.MethodGet, Prefix+"/boards")
	if status != http.StatusOK || strings.TrimSpace(body) != `{"boards":[]}` {
		t.Fatalf("/boards = %d %s, want an empty list", status, body)
	}
}

func TestABoardsAPIAnswersThroughThePrefix(t *testing.T) {
	home := t.TempDir()
	repo := newRepo(t, home, "alpha")
	writeTicket(t, repo, "ready", "TAB-1")
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repo), Version: "test", Now: pinnedNow,
	})
	key := server.KeyOf(boardRoot(repo))
	status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board")
	if status != http.StatusOK {
		t.Fatalf("the board through the prefix = %d %s, want 200", status, body)
	}
	// the same bytes the board's own app answers: the tab mounts the app, not a
	// re-reading of the board
	direct := server.NewApp(server.Options{Board: board.Board{Root: boardRoot(repo)}, Version: "test", Now: pinnedNow})
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	direct.Run(runCtx)
	wantStatus, wantBody := answer(t, direct.API(), http.MethodGet, "/api/board")
	if wantStatus != http.StatusOK || body != wantBody {
		t.Fatalf("the board through the prefix = %s, want the app's own %s", body, wantBody)
	}
	status, body = answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/tickets/TAB-1")
	if status != http.StatusOK || !strings.Contains(body, "Body text.") {
		t.Fatalf("the ticket through the prefix = %d %s, want the ticket whole", status, body)
	}
}

func TestAnUnknownKeyIs404(t *testing.T) {
	tab := Mount(context.Background(), Options{Roots: staticRoots()})
	status, body := answer(t, tab, http.MethodGet, Prefix+"/b/000000000000/api/board")
	if status != http.StatusNotFound || !strings.Contains(body, "no board 000000000000") {
		t.Fatalf("an unknown key = %d %s, want 404", status, body)
	}
	for _, path := range []string{Prefix, Prefix + "/nope", Prefix + "/b/"} {
		status, body = answer(t, tab, http.MethodGet, path)
		if status != http.StatusNotFound || !strings.Contains(body, "not found: "+path) {
			t.Fatalf("%s = %d %s, want kanban's 404", path, status, body)
		}
	}
	if status, _ := answer(t, tab, http.MethodPost, Prefix+"/boards"); status != http.StatusMethodNotAllowed {
		t.Fatalf("POST /boards = %d, want 405", status)
	}
}

// streamRecorder signals its first written byte: /api/events subscribes the
// client before it writes the snapshot frame, so the signal proves the
// subscription is live without a race on the buffer. WriteString is shadowed
// so the event stream's io.WriteString cannot slip past the signal.
type streamRecorder struct {
	httptest.ResponseRecorder
	open chan struct{}
	once sync.Once
}

func (w *streamRecorder) Write(b []byte) (int, error) {
	w.once.Do(func() { close(w.open) })
	return w.ResponseRecorder.Write(b)
}

func (w *streamRecorder) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

func TestStopEndsEveryWatcher(t *testing.T) {
	home := t.TempDir()
	repoA := newRepo(t, home, "alpha")
	repoB := newRepo(t, home, "beta")
	tab := Mount(context.Background(), Options{
		Roots: staticRoots(repoA, repoB), Version: "test", Now: pinnedNow,
	})
	keyA := server.KeyOf(boardRoot(repoA))
	keyB := server.KeyOf(boardRoot(repoB))
	// the first request mounts each board and starts its watch
	for _, key := range []string{keyA, keyB} {
		if status, body := answer(t, tab, http.MethodGet, Prefix+"/b/"+key+"/api/board"); status != http.StatusOK {
			t.Fatalf("board %s = %d %s, want 200", key, status, body)
		}
	}
	tab.mu.Lock()
	appA, appB := tab.apps[keyA].app, tab.apps[keyB].app
	tab.mu.Unlock()

	// each board's event stream is open, and a ticket lands on the watched view
	streams := make([]chan struct{}, 0, 2)
	for _, key := range []string{keyA, keyB} {
		request := httptest.NewRequest(http.MethodGet, Prefix+"/b/"+key+"/api/events", nil)
		recorder := &streamRecorder{open: make(chan struct{})}
		served := make(chan struct{})
		go func() {
			defer close(served)
			tab.ServeHTTP(recorder, request)
		}()
		select {
		case <-recorder.open:
		case <-time.After(5 * time.Second):
			t.Fatal("the stream never opened")
		}
		streams = append(streams, served)
	}
	for repo, id := range map[string]string{repoA: "STOP-A", repoB: "STOP-B"} {
		writeTicket(t, repo, "ready", id)
	}
	if !boardShows(t, appA, "STOP-A") || !boardShows(t, appB, "STOP-B") {
		t.Fatal("the watches never picked up their tickets")
	}

	tab.Stop()
	for i, served := range streams {
		select {
		case <-served:
		case <-time.After(3 * time.Second):
			t.Fatalf("board %d's stream never ended", i)
		}
	}
	// the watches stopped with the streams: a ticket written now never shows
	writeTicket(t, repoA, "ready", "STOP-A2")
	time.Sleep(time.Duration(4) * 300 * time.Millisecond)
	if status, body := answer(t, appA.API(), http.MethodGet, "/api/board"); strings.Contains(body, "STOP-A2") {
		t.Fatalf("the watch ran on after Stop: %d %s", status, body)
	}
	tab.Stop() // a second Stop is a no-op
}

// boardShows answers whether the ticket id reached one app's watched snapshot
// within three seconds.
func boardShows(t *testing.T, app *server.App, id string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, body := answer(t, app.API(), http.MethodGet, "/api/board")
		if strings.Contains(body, id) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}
