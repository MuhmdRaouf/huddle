package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	"github.com/MuhmdRaouf/huddle/internal/kanban/web"
)

// The HTTP surface: every route of the spec, the loopback guards, the token
// check and the static fallback, over a real loopback listener and over
// hand-built requests for the guards.

// newBoard answers one throwaway board: the seven columns, EPOCH and LOG.
func newBoard(t *testing.T) board.Board {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".agents", "kanban")
	for _, col := range board.Columns {
		if err := os.MkdirAll(filepath.Join(root, col), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "EPOCH"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "LOG"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	return board.Board{Root: root}
}

// writeTicket lands one minimal ticket file in a column.
func writeTicket(t *testing.T, b board.Board, col, id string) {
	t.Helper()
	text := "---\nid: " + id + "\ntitle: \"" + id + " title\"\ntype: docs\ncluster: kanban\n" +
		"summary: \"one\"\n---\n\n# " + id + "\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(b.Root, col, id+".md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitState polls the board's state file until it appears, and answers it.
func waitState(t *testing.T, home, boardPath string) State {
	t.Helper()
	key := KeyOf(boardPath)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if st := ReadState(home, key); st != nil {
			return *st
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the server never wrote its state file")
	return State{}
}

// newApp builds one app over a throwaway board and serves it on a loopback
// listener, port known to the Host guard.
func newApp(t *testing.T) (*App, *httptest.Server, board.Board) {
	t.Helper()
	b := newBoard(t)
	app := NewApp(Options{Board: b, Home: t.TempDir(), Version: "test", EveryMs: 25})
	return app, listenApp(t, app), b
}

// listenApp serves one built app on a loopback listener and tells the Host
// guard which port that is.
func listenApp(t *testing.T, app *App) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(app.Handler())
	app.SetPort(ts.Listener.Addr().(*net.TCPAddr).Port)
	t.Cleanup(ts.Close)
	return ts
}

// renameFile moves one file, the way a hand move does.
func renameFile(from, to string) error {
	return os.Rename(from, to)
}

// getJSON fetches one path and answers status and body.
func getJSON(t *testing.T, url string) (int, []byte) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := response.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	return response.StatusCode, body
}

func TestRoutes(t *testing.T) {
	app, ts, b := newApp(t)
	writeTicket(t, b, "ready", "ROU-1")
	if _, changed := app.watcher.Scan(context.Background()); !changed {
		t.Fatal("the first scan changed nothing")
	}
	base := ts.URL

	t.Run("health answers ok, the board, the version and the pid", func(t *testing.T) {
		status, body := getJSON(t, base+"/api/health")
		if status != 200 {
			t.Fatalf("status = %d, want 200", status)
		}
		var health struct {
			OK      bool   `json:"ok"`
			Board   string `json:"board"`
			Version string `json:"version"`
			Pid     int    `json:"pid"`
		}
		if err := json.Unmarshal(body, &health); err != nil {
			t.Fatalf("decode %q: %v", body, err)
		}
		abs := absPath(b.Root)
		if !health.OK || health.Board != abs || health.Version != "test" || health.Pid != os.Getpid() {
			t.Errorf("health = %+v", health)
		}
	})

	t.Run("board answers the snapshot", func(t *testing.T) {
		status, body := getJSON(t, base+"/api/board")
		if status != 200 {
			t.Fatalf("status = %d, want 200", status)
		}
		var snap struct {
			Rev      int    `json:"rev"`
			Board    string `json:"board"`
			Project  string `json:"project"`
			Epoch    int    `json:"epoch"`
			StopLine any    `json:"stopLine"`
			Columns  []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
				Count int    `json:"count"`
			} `json:"columns"`
			Cards []struct {
				ID     string `json:"id"`
				Column string `json:"column"`
				Title  string `json:"title"`
			} `json:"cards"`
		}
		if err := json.Unmarshal(body, &snap); err != nil {
			t.Fatalf("decode %q: %v", body, err)
		}
		if snap.Rev < 1 || snap.Board != app.watcher.Snapshot().Board {
			t.Errorf("rev/board = %d/%q", snap.Rev, snap.Board)
		}
		if snap.Epoch != 1 || snap.StopLine != nil {
			t.Errorf("epoch/stopLine = %d/%v", snap.Epoch, snap.StopLine)
		}
		if len(snap.Columns) != 7 || snap.Columns[0].ID != "backlog" || snap.Columns[0].Title != "Backlog" {
			t.Errorf("columns = %+v", snap.Columns)
		}
		if len(snap.Cards) != 1 || snap.Cards[0].ID != "ROU-1" || snap.Cards[0].Column != "ready" {
			t.Errorf("cards = %+v", snap.Cards)
		}
		if want := filepath.Base(filepath.Dir(filepath.Dir(b.Root))); snap.Project != want {
			t.Errorf("project = %q, want %q", snap.Project, want)
		}
	})

	t.Run("tickets answers one card whole", func(t *testing.T) {
		status, body := getJSON(t, base+"/api/tickets/ROU-1")
		if status != 200 {
			t.Fatalf("status = %d, want 200", status)
		}
		var doc struct {
			Card struct {
				ID     string `json:"id"`
				Column string `json:"column"`
			} `json:"card"`
			Fields map[string]string `json:"fields"`
			Body   string            `json:"body"`
			Path   string            `json:"path"`
		}
		if err := json.Unmarshal(body, &doc); err != nil {
			t.Fatalf("decode %q: %v", body, err)
		}
		if doc.Card.ID != "ROU-1" || doc.Card.Column != "ready" {
			t.Errorf("card = %+v", doc.Card)
		}
		if doc.Fields["cluster"] != "kanban" {
			t.Errorf("fields = %v", doc.Fields)
		}
		if !strings.Contains(doc.Body, "# ROU-1") {
			t.Errorf("body = %q", doc.Body)
		}
		if !strings.HasSuffix(doc.Path, filepath.Join("ready", "ROU-1.md")) {
			t.Errorf("path = %q", doc.Path)
		}
	})

	t.Run("tickets answers 404 for an unknown id", func(t *testing.T) {
		status, body := getJSON(t, base+"/api/tickets/NOPE-1")
		if status != 404 {
			t.Fatalf("status = %d, want 404", status)
		}
		var answer struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(body, &answer); err != nil || answer.Error == "" {
			t.Errorf("body = %q, want an error object", body)
		}
	})

	t.Run("refresh rescans and answers the rev", func(t *testing.T) {
		response, err := http.Post(base+"/api/refresh", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", response.StatusCode)
		}
		var answer struct {
			Rev int64 `json:"rev"`
		}
		if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
			t.Fatal(err)
		}
		if answer.Rev < 1 {
			t.Errorf("rev = %d, want at least 1", answer.Rev)
		}
	})

	t.Run("an unknown api path answers 404 json", func(t *testing.T) {
		status, body := getJSON(t, base+"/api/nope")
		if status != 404 {
			t.Fatalf("status = %d, want 404", status)
		}
		if !strings.Contains(string(body), `"error"`) {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("a wrong method answers 405 json", func(t *testing.T) {
		status, body := getJSON(t, base+"/api/health")
		if status != 200 {
			t.Fatalf("GET health status = %d", status)
		}
		response, err := http.Post(base+"/api/health", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != 405 {
			t.Fatalf("POST health status = %d, want 405", response.StatusCode)
		}
		_ = body
	})
}

func TestStaticServesTheEmbeddedPage(t *testing.T) {
	_, ts, _ := newApp(t)
	base := ts.URL

	t.Run("/ serves index.html", func(t *testing.T) {
		status, body := getJSON(t, base+"/")
		if status != 200 {
			t.Fatalf("status = %d", status)
		}
		if !strings.Contains(string(body), "<!doctype html>") || !strings.Contains(string(body), "Kanban") {
			t.Errorf("body = %q, want the embedded page", body)
		}
	})

	t.Run("/ serves the built board page", func(t *testing.T) {
		status, body := getJSON(t, base+"/")
		if status != 200 {
			t.Fatalf("status = %d", status)
		}
		page := string(body)
		if !strings.Contains(page, `<div id="root">`) {
			t.Errorf("body lacks the mount point div #root: %q", page)
		}
		if !strings.Contains(page, `<script type="module"`) {
			t.Errorf("body lacks the module script that boots the page: %q", page)
		}
	})

	t.Run("an unknown non-api path serves index.html", func(t *testing.T) {
		status, body := getJSON(t, base+"/anything/else")
		if status != 200 {
			t.Fatalf("status = %d, want 200", status)
		}
		if !strings.Contains(string(body), "<!doctype html>") {
			t.Errorf("body = %q, want the index fallback", body)
		}
	})

	t.Run("index.html is served by name", func(t *testing.T) {
		status, body := getJSON(t, base+"/index.html")
		if status != 200 {
			t.Fatalf("status = %d", status)
		}
		if !strings.Contains(string(body), "Kanban") {
			t.Errorf("body = %q", body)
		}
	})

	t.Run("every asset under assets/ answers 200 with its content type", func(t *testing.T) {
		wanted := map[string]string{
			".js":  "text/javascript; charset=utf-8",
			".css": "text/css; charset=utf-8",
		}
		found := map[string]string{} // file extension -> the name to fetch
		err := fs.WalkDir(web.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || !strings.HasPrefix(name, "assets/") {
				return err
			}
			for ext := range wanted {
				if strings.HasSuffix(name, ext) {
					found[ext] = name
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != len(wanted) {
			t.Fatalf("assets/ holds %d of %d kinds, want one file per kind", len(found), len(wanted))
		}
		for ext, name := range found {
			response, err := http.Get(base + "/" + name)
			if err != nil {
				t.Fatalf("GET %s: %v", name, err)
			}
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatalf("read %s: %v", name, readErr)
			}
			if response.StatusCode != 200 {
				t.Errorf("GET %s = %d, want 200", name, response.StatusCode)
			}
			if got := response.Header.Get("Content-Type"); got != wanted[ext] {
				t.Errorf("GET %s content type = %q, want %q", name, got, wanted[ext])
			}
			if len(body) == 0 {
				t.Errorf("GET %s body is empty", name)
			}
		}
	})
}

// guardRequest builds one request the handler answers without a socket: the
// Host and remote address are the two things the guard reads.
func guardRequest(app *App, host, remote string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = host
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func TestLoopbackGuards(t *testing.T) {
	app, ts, _ := newApp(t)
	port := ts.Listener.Addr().(*net.TCPAddr).Port

	t.Run("the board's own host passes", func(t *testing.T) {
		for _, host := range []string{
			fmt.Sprintf("127.0.0.1:%d", port), fmt.Sprintf("localhost:%d", port),
		} {
			if rec := guardRequest(app, host, "127.0.0.1:55555"); rec.Code != 200 {
				t.Errorf("host %s = %d, want 200", host, rec.Code)
			}
		}
	})

	t.Run("a foreign host is refused", func(t *testing.T) {
		rec := guardRequest(app, fmt.Sprintf("evil.example:%d", port), "127.0.0.1:55555")
		if rec.Code != 403 {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("body = %q, want an error object", rec.Body.String())
		}
	})

	t.Run("a non-loopback remote address is refused", func(t *testing.T) {
		rec := guardRequest(app, fmt.Sprintf("127.0.0.1:%d", port), "10.1.2.3:4444")
		if rec.Code != 403 {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

func TestShutdownIsTokenChecked(t *testing.T) {
	app, ts, _ := newApp(t)
	host := fmt.Sprintf("127.0.0.1:%d", ts.Listener.Addr().(*net.TCPAddr).Port)

	shutdown := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/shutdown", nil)
		req.Host = host
		req.RemoteAddr = "127.0.0.1:55555"
		if token != "" {
			req.Header.Set("X-Kanban-Token", token)
		}
		rec := httptest.NewRecorder()
		app.Handler().ServeHTTP(rec, req)
		return rec
	}

	t.Run("without a token it is 403", func(t *testing.T) {
		if rec := shutdown(""); rec.Code != 403 {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("with a wrong token it is 403", func(t *testing.T) {
		if rec := shutdown("wrong"); rec.Code != 403 {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})

	t.Run("with the token it answers 200 and asks the app to stop", func(t *testing.T) {
		if rec := shutdown(app.token); rec.Code != 200 {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		select {
		case reason := <-app.stopReason:
			if reason != ReasonStop {
				t.Errorf("reason = %q, want %q", reason, ReasonStop)
			}
		case <-time.After(time.Second):
			t.Fatal("the shutdown never reached the app")
		}
	})
}
