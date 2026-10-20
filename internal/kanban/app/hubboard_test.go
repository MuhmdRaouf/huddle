package app

// The hub's board link: the word names the hub page for the working directory's
// board when a hub answers, and start and open start the hub through huddle's
// own EnsureHub when none does — which, for a home that was never set up, is a
// refusal, never a server started behind the user's back.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
)

// fakeHub answers /health the way the flow client's probe reads it, and names
// its port for the home's port file.
func fakeHub(t *testing.T, healthy bool) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" && healthy {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Port()
}

// writeHomePort lays the port file a hub leaves behind.
func writeHomePort(t *testing.T, hubHome, port string) {
	t.Helper()
	if err := os.MkdirAll(home.Home{Root: hubHome}.Run(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(home.Home{Root: hubHome}.PortFile(), []byte(port), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHubBoardNamesTheHubsPage(t *testing.T) {
	hubHome := t.TempDir()
	getenv := func(key string) (string, bool) {
		if key == "HUDDLE_HOME" {
			return hubHome, true
		}
		return "", false
	}
	const key = "abc123def456"

	t.Run("a healthy hub answers its page with the key as the hash", func(t *testing.T) {
		writeHomePort(t, hubHome, fakeHub(t, true))
		link, ok := hubBoard(context.Background(), getenv, key)
		if !ok {
			t.Fatal("a healthy hub answers no link")
		}
		if want := strings.TrimSuffix(hubAddress(t, hubHome), "/") + "/#/kanban/" + key; link != want {
			t.Errorf("the link = %q, want %q", link, want)
		}
	})
	t.Run("no port file is no link", func(t *testing.T) {
		if _, ok := hubBoard(context.Background(), getenv, key); ok {
			t.Fatal("a home without a port file counts as a hub")
		}
	})
	t.Run("a hub that never answers health is no link", func(t *testing.T) {
		writeHomePort(t, hubHome, fakeHub(t, false))
		if _, ok := hubBoard(context.Background(), getenv, key); ok {
			t.Fatal("a silent hub counts as a hub")
		}
	})
}

// hubAddress reads the base url the port file's port spells.
func hubAddress(t *testing.T, hubHome string) string {
	t.Helper()
	raw, err := os.ReadFile(home.Home{Root: hubHome}.PortFile())
	if err != nil {
		t.Fatal(err)
	}
	return "http://127.0.0.1:" + strings.TrimSpace(string(raw))
}

// TestOpenNamesTheHubsBoardPageAndRefusesToStartAHub runs the kanban word
// itself: with a hub answering, open names its page and starts nothing; with
// the hub gone and a huddle home that was never set up, the same word is a
// refusal to run setup, and no hub is started behind the user's back.
func TestOpenNamesTheHubsBoardPageAndRefusesToStartAHub(t *testing.T) {
	hubHome := t.TempDir()
	repo := repoWithOrigin(t, "work", "git@github.com:MuhmdRaouf/open-board.git")
	root := filepath.Join(repo, ".agents", "kanban")
	if _, err := board.Init(context.Background(), root, "", time.Now()); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HUDDLE_HOME", hubHome)
	t.Setenv("KANBAN_NO_OPEN", "1")
	t.Chdir(repo)

	// a hub answers: the word names its page
	writeHomePort(t, hubHome, fakeHub(t, true))
	var out, errOut bytes.Buffer
	if code := Run([]string{"open"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("open with a hub = %d, out %q, err %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "/#/kanban/") {
		t.Errorf("open printed %q, want the hub page's link", out.String())
	}

	// the hub is gone and the home was never set up: refused, nothing started
	if err := os.Remove(home.Home{Root: hubHome}.PortFile()); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "never-set-up")
	t.Setenv("HUDDLE_HOME", missing)
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"open"}, &out, &errOut, "test"); code != 6 {
		t.Fatalf("open without a set-up home = %d, out %q, err %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(errOut.String(), "not set up") {
		t.Errorf("the refusal = %q, want the setup message", errOut.String())
	}
}
