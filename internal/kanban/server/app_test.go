package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// The unit halves: port binding, the health probe, the static files, the JSON
// encoder, the hub's slow-client rule, and the lifecycle's failure shapes.

func TestBindPort(t *testing.T) {
	t.Run("a pinned port binds exactly that port", func(t *testing.T) {
		free := closedPort(t)
		listener, port, err := bindPort(free)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		if port != free {
			t.Errorf("port = %d, want %d", port, free)
		}
	})

	t.Run("a pinned port another program holds fails at once", func(t *testing.T) {
		held, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = held.Close() }()
		heldPort := held.Addr().(*net.TCPAddr).Port
		listener, _, err := bindPort(heldPort)
		if err == nil {
			_ = listener.Close()
			t.Fatal("a held port bound twice")
		}
	})

	t.Run("below zero lets the OS pick", func(t *testing.T) {
		listener, port, err := bindPort(-1)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		if port < 1 || port > 65535 {
			t.Errorf("port = %d, want an OS pick", port)
		}
	})
}

func TestHealthyAt(t *testing.T) {
	b := newBoard(t)
	abs := absPath(b.Root)
	app := NewApp(Options{Board: b, Home: t.TempDir(), Version: "test"})
	listenApp(t, app)

	t.Run("the board's own server answers true", func(t *testing.T) {
		if !healthyAt(context.Background(), app.Port, abs) {
			t.Error("the healthy server answered false")
		}
	})

	t.Run("another board answers false", func(t *testing.T) {
		if healthyAt(context.Background(), app.Port, abs+"/elsewhere") {
			t.Error("another board's server answered true")
		}
	})

	t.Run("a page that is not health answers false", func(t *testing.T) {
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer other.Close()
		port := other.Listener.Addr().(*net.TCPAddr).Port
		if healthyAt(context.Background(), port, abs) {
			t.Error("a 404 answered true")
		}
	})

	t.Run("garbage that parses as 200 answers false", func(t *testing.T) {
		other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("not json"))
		}))
		defer other.Close()
		port := other.Listener.Addr().(*net.TCPAddr).Port
		if healthyAt(context.Background(), port, abs) {
			t.Error("garbage answered true")
		}
	})

	t.Run("nothing listening answers false", func(t *testing.T) {
		if healthyAt(context.Background(), closedPort(t), abs) {
			t.Error("a closed port answered true")
		}
	})
}

func TestStaticAndTypes(t *testing.T) {
	b := newBoard(t)
	app := NewApp(Options{Board: b, Home: t.TempDir(), Version: "test"})
	// a built page with one asset beside the index, the shape the real build
	// will have
	app.files = fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>Kanban</title>")},
		"app.js":     &fstest.MapFile{Data: []byte("console.log('kanban')")},
		"app.css":    &fstest.MapFile{Data: []byte("body{}")},
		"logo.svg":   &fstest.MapFile{Data: []byte("<svg/>")},
	}
	ts := listenApp(t, app)

	for path, wantType := range map[string]string{
		"/":             "text/html; charset=utf-8",
		"/index.html":   "text/html; charset=utf-8",
		"/app.js":       "text/javascript; charset=utf-8",
		"/app.css":      "text/css; charset=utf-8",
		"/logo.svg":     "image/svg+xml",
		"/no/such/page": "text/html; charset=utf-8", // the index owns the routes
	} {
		response, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body := make([]byte, 64)
		n, _ := response.Body.Read(body)
		_ = response.Body.Close()
		if got := response.Header.Get("Content-Type"); got != wantType {
			t.Errorf("%s content type = %q, want %q", path, got, wantType)
		}
		if response.StatusCode != 200 || n == 0 {
			t.Errorf("%s = %d with %q", path, response.StatusCode, body[:n])
		}
	}

	t.Run("unknown types answer octets", func(t *testing.T) {
		if got := contentTypeOf("data.bin"); got != "application/octet-stream" {
			t.Errorf("contentTypeOf = %q", got)
		}
	})
}

func TestEncodeCompact(t *testing.T) {
	t.Run("angle brackets stay raw", func(t *testing.T) {
		encoded := string(encodeCompact(map[string]string{"q": "<b>&</b>"}))
		if strings.Contains(encoded, "u003c") || strings.Contains(encoded, "u0026") {
			t.Errorf("encoded = %q, want raw brackets and ampersands", encoded)
		}
		if !strings.Contains(encoded, "<b>&</b>") {
			t.Errorf("encoded = %q, want the raw text", encoded)
		}
	})

	t.Run("a body that cannot encode answers null", func(t *testing.T) {
		if got := string(encodeCompact(func() {})); got != "null" {
			t.Errorf("encoded = %q, want null", got)
		}
	})
}

func TestHubDropsASlowPage(t *testing.T) {
	h := &hub{conns: map[*sseClient]struct{}{}}

	t.Run("a page that cannot keep up is dropped", func(t *testing.T) {
		client := newClient()
		client.frames = make(chan sseFrame, 1) // room for one frame only
		h.subscribe(client)
		h.publish(sseFrame{name: "delta"})
		h.publish(sseFrame{name: "delta"}) // the queue is full: the page goes
		select {
		case <-client.dead:
		case <-time.After(time.Second):
			t.Fatal("the slow page was never dropped")
		}
		h.unsubscribe(client)
	})

	t.Run("closeAll ends every page", func(t *testing.T) {
		first, second := newClient(), newClient()
		h.subscribe(first)
		h.subscribe(second)
		h.closeAll()
		select {
		case <-first.dead:
		case <-time.After(time.Second):
			t.Fatal("closeAll left a page open")
		}
		select {
		case <-second.dead:
		case <-time.After(time.Second):
			t.Fatal("closeAll left a page open")
		}
	})
}

func TestAllowedRefusesARemoteWithoutAPort(t *testing.T) {
	app, ts, _ := newApp(t)
	port := ts.Listener.Addr().(*net.TCPAddr).Port
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = fmt.Sprintf("127.0.0.1:%d", port)
	req.RemoteAddr = "localhost" // no address:port pair
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestRefreshPublishesTheChangeItSees(t *testing.T) {
	app, ts, b := newApp(t)
	writeTicket(t, b, "backlog", "CHA-1")
	if _, changed := app.watcher.Scan(context.Background()); !changed {
		t.Fatal("the first scan changed nothing")
	}
	writeTicket(t, b, "backlog", "CHA-2")
	response, err := http.Post(ts.URL+"/api/refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	var answer struct {
		Rev int64 `json:"rev"`
	}
	if err := decodeJSONBody(response.Body, &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Rev != 2 {
		t.Errorf("rev = %d, want 2 (the scan saw the new ticket)", answer.Rev)
	}
}

func TestServeRefusesAHomeItCannotWrite(t *testing.T) {
	b := newBoard(t)
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Serve(context.Background(), Options{
		Board: b, Home: filepath.Join(blocked, "state"), Version: "test", Port: -1,
	}); err == nil {
		t.Fatal("an unwritable home served anyway")
	}
}

func TestStartWithoutAServerThatListensFails(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	fx := DefaultEffects()
	fx.Sleep = func(int64) {} // the whole budget at once
	fx.SpawnServe = func(string, []string, string) int64 { return 0 }
	_, err := Start(context.Background(), b.Root, home, -1, fx)
	if err == nil {
		t.Fatal("a start whose child never listened answered healthy")
	}
	if !strings.Contains(err.Error(), "did not become healthy") {
		t.Errorf("error = %v", err)
	}
}

func TestRefreshAndStopOverBrokenAnswers(t *testing.T) {
	b := newBoard(t)
	home := t.TempDir()
	ctx := context.Background()
	abs := absPath(b.Root)
	key := KeyOf(abs)
	st := State{Pid: 1, Port: 45678, Board: abs, Token: "tok"}
	if err := WriteState(ctx, home, key, st); err != nil {
		t.Fatal(err)
	}

	t.Run("a fetch that fails removes the record", func(t *testing.T) {
		fx := DefaultEffects()
		fx.Fetch = func(context.Context, string, string, map[string]string) (int, []byte, bool) {
			return 0, nil, false
		}
		if _, err := Refresh(ctx, b.Root, home, fx); !errorsIsNoServer(err) {
			t.Fatalf("refresh = %v, want no server", err)
		}
		if ReadState(home, key) != nil {
			t.Error("the dead record survived")
		}
	})

	t.Run("an answer that is not json is no server", func(t *testing.T) {
		if err := WriteState(ctx, home, key, st); err != nil {
			t.Fatal(err)
		}
		fx := DefaultEffects()
		fx.Fetch = func(context.Context, string, string, map[string]string) (int, []byte, bool) {
			return 200, []byte("nope"), true
		}
		if _, err := Refresh(ctx, b.Root, home, fx); !errorsIsNoServer(err) {
			t.Fatalf("refresh = %v, want no server", err)
		}
	})

	t.Run("a refused stop removes the record", func(t *testing.T) {
		if err := WriteState(ctx, home, key, st); err != nil {
			t.Fatal(err)
		}
		fx := DefaultEffects()
		fx.Fetch = func(context.Context, string, string, map[string]string) (int, []byte, bool) {
			return 403, []byte(`{"error":"forbidden token"}`), true
		}
		if err := Stop(ctx, b.Root, home, fx); !errorsIsNoServer(err) {
			t.Fatalf("stop = %v, want no server", err)
		}
		if ReadState(home, key) != nil {
			t.Error("the refused record survived")
		}
	})
}

func TestEffects(t *testing.T) {
	t.Run("ErrNoServer names its board", func(t *testing.T) {
		if got := (&ErrNoServer{Board: "/b"}).Error(); got != "no server for /b" {
			t.Errorf("Error = %q", got)
		}
	})

	t.Run("browserOpener answers one on every platform", func(t *testing.T) {
		if browserOpener() == "" {
			t.Error("no browser opener on this platform")
		}
	})

	t.Run("spawnServe starts a process and takes its log", func(t *testing.T) {
		home := t.TempDir()
		log := LogPath(home, "k")
		if pid := spawnServe("/bin/sh", []string{"-c", "echo started"}, log); pid == 0 {
			t.Fatal("the spawn failed")
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(log); err == nil && strings.Contains(string(data), "started") {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the child's output never reached the log")
	})

	t.Run("fetch refuses a request it cannot build", func(t *testing.T) {
		if _, _, ok := fetch(context.Background(), "BAD METHOD", "http://127.0.0.1:1/", nil); ok {
			t.Error("an unbuilable request answered ok")
		}
	})
}

// errorsIsNoServer answers whether err is the no-server refusal.
func errorsIsNoServer(err error) bool {
	var noServer *ErrNoServer
	return errors.As(err, &noServer)
}

// decodeJSONBody reads one response body into v.
func decodeJSONBody(body io.Reader, v any) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

var _ = board.Columns
