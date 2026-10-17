package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The mounted surface: the read routes the page loads with, answered by a hand
// the Host guard never allows, for a server that mounts the board inside its
// own — and the watch run that keeps the snapshot fresh with no listener and
// no port.

// apiGet answers one request through a handler whose Host guard would refuse
// the request, the way a mounting server's forwards arrive.
func apiGet(t *testing.T, h http.Handler, method, path string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(method, path, nil)
	request.Host = "hub.example"
	recorder := httptest.NewRecorder()
	h.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

func TestAPIServesTheBoardWithoutTheHostCheck(t *testing.T) {
	b := newBoard(t)
	writeTicket(t, b, "ready", "API-1")
	app := NewApp(Options{Board: b, Version: "test"})
	if _, changed := app.watcher.Scan(context.Background()); !changed {
		t.Fatal("the first scan changed nothing")
	}
	status, body := apiGet(t, app.API(), http.MethodGet, "/api/board")
	if status != http.StatusOK || !strings.Contains(body, "API-1") {
		t.Fatalf("/api/board over the mounted API = %d %s, want the snapshot", status, body)
	}
	status, body = apiGet(t, app.API(), http.MethodGet, "/api/tickets/API-1")
	if status != http.StatusOK || !strings.Contains(body, `"id":"API-1"`) {
		t.Fatalf("/api/tickets/API-1 over the mounted API = %d %s, want the ticket", status, body)
	}
	// the same hand over the standalone server is the Host guard's refusal: the
	// mount, not the board app, owns host and credential
	request := httptest.NewRequest(http.MethodGet, "/api/board", nil)
	request.Host = "hub.example"
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("the same host over ServeHTTP = %d, want 403", recorder.Code)
	}
}

func TestAPIRefusesWhatOnlyTheStandaloneServerAnswers(t *testing.T) {
	b := newBoard(t)
	app := NewApp(Options{Board: b, Version: "test"})
	for _, path := range []string{"/api/health", "/api/refresh", "/api/shutdown", "/"} {
		status, body := apiGet(t, app.API(), http.MethodGet, path)
		if status != http.StatusNotFound || !strings.Contains(body, "not found: "+path) {
			t.Fatalf("GET %s over the mounted API = %d %s, want 404", path, status, body)
		}
		status, _ = apiGet(t, app.API(), http.MethodPost, path)
		if status != http.StatusNotFound {
			t.Fatalf("POST %s over the mounted API = %d, want 404", path, status)
		}
	}
}

func TestRunWatchesWithoutListening(t *testing.T) {
	b := newBoard(t)
	app := NewApp(Options{Board: b, Version: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	app.Run(ctx)
	if app.Port != 0 {
		t.Fatalf("port = %d, want none: Run binds no port", app.Port)
	}
	writeTicket(t, b, "ready", "RUN-1")
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, body := apiGet(t, app.API(), http.MethodGet, "/api/board")
		if status == http.StatusOK && strings.Contains(body, "RUN-1") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the ticket never showed on the watched board: %d %s", status, body)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
