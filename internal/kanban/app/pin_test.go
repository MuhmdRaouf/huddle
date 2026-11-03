package app

// A session's board pin: the hub keeps it when one answers, and a file in the sessions directory
// keeps it when none does, so a worker without a hub keeps its pin.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
)

// pinWorld is one environment: a huddle home and a sessions directory of its own, and a token.
type pinWorld struct {
	home     string
	sessions string
}

func newPinWorld(t *testing.T) pinWorld {
	t.Helper()
	w := pinWorld{home: t.TempDir(), sessions: t.TempDir()}
	if err := os.MkdirAll(home.Home{Root: w.home}.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w pinWorld) getenv(key string) (string, bool) {
	switch key {
	case "HUDDLE_HOME":
		return w.home, true
	case "HUDDLE_SESSIONS_DIR":
		return w.sessions, true
	case "HUDDLE_TOKEN":
		return "test-token", true
	}
	return "", false
}

// hubAt starts a fake hub and writes its port where the kanban word looks.
func (w pinWorld) hubAt(t *testing.T, serve http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(serve)
	t.Cleanup(srv.Close)
	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home.Home{Root: w.home}.PortFile(), []byte(parsed.Port()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAPinWithNoHubLivesInAFileOfTheSessionsDirectory(t *testing.T) {
	w := newPinWorld(t)
	ctx := context.Background()
	if got := readPin(ctx, w.getenv, "s1"); got != "" {
		t.Fatalf("pin before any = %q, want none", got)
	}
	if err := writePin(ctx, w.getenv, "s1", "login"); err != nil {
		t.Fatalf("write the pin: %v", err)
	}
	if got := readPin(ctx, w.getenv, "s1"); got != "login" {
		t.Errorf("pin = %q, want login", got)
	}
	if got := readPin(ctx, w.getenv, "s2"); got != "" {
		t.Errorf("pin of another session = %q, want none", got)
	}
	entries, _ := os.ReadDir(w.sessions)
	if len(entries) != 1 {
		t.Errorf("the sessions directory holds %d files, want the one pin file", len(entries))
	}
}

func TestAPinFileNameCannotLeaveTheSessionsDirectory(t *testing.T) {
	w := newPinWorld(t)
	if err := writePin(context.Background(), w.getenv, "../../escape", "login"); err != nil {
		t.Fatalf("write the pin: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.sessions, "..", "..", "escape.board")); err == nil {
		t.Error("the pin file left the sessions directory")
	}
}

func TestTheHubsPinWinsAndAWriteGoesToBoth(t *testing.T) {
	w := newPinWorld(t)
	ctx := context.Background()
	pinned := ""
	w.hubAt(t, func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health":
		case r.URL.Path == "/api/registry/sessions/s1/board" && r.Method == http.MethodPost:
			var body struct {
				Board string `json:"board"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			pinned = body.Board
			_, _ = rw.Write([]byte(`{"board":"` + pinned + `"}`))
		case r.URL.Path == "/api/registry/sessions/s1/board" && r.Method == http.MethodGet:
			if got := r.Header.Get("x-huddle-token"); got != "test-token" {
				t.Errorf("the token header = %q, want the test token", got)
			}
			_, _ = rw.Write([]byte(`{"board":"` + pinned + `"}`))
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	})
	if err := writePin(ctx, w.getenv, "s1", "login"); err != nil {
		t.Fatalf("write the pin: %v", err)
	}
	if pinned != "login" {
		t.Errorf("the hub holds %q, want login", pinned)
	}
	pinned = "billing" // the owner changes the pin from the session row
	if got := readPin(ctx, w.getenv, "s1"); got != "billing" {
		t.Errorf("pin = %q, want the hub's billing over the file's login", got)
	}
}

func TestAHubThatKnowsNoPinLeavesTheFilesPin(t *testing.T) {
	w := newPinWorld(t)
	ctx := context.Background()
	if err := writePin(ctx, w.getenv, "s1", "login"); err != nil {
		t.Fatalf("write the pin with no hub: %v", err)
	}
	w.hubAt(t, func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			return
		}
		rw.WriteHeader(http.StatusNotFound) // the hub has no row for the session
	})
	if got := readPin(ctx, w.getenv, "s1"); got != "login" {
		t.Errorf("pin = %q, want the file's login when the hub knows none", got)
	}
}
