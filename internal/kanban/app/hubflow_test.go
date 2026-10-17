package app

// The flow client the kanban word carries: one command goes to the local
// huddle's kanban tab when a hub answers, and only the verdicts the tab defines
// count as handled — everything else leaves the board's own files to answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/huddle/home"
	homeserver "github.com/MuhmdRaouf/huddle/internal/huddle/server"
	"github.com/MuhmdRaouf/huddle/internal/kanban/board"
	kanbanserver "github.com/MuhmdRaouf/huddle/internal/kanban/server"
)

// TestHubFlowAnswersOnlyAVerdictItRecognises runs the flow client against one
// fake hub: only a 200 with lines and a 409 refusal are handled; anything else
// — another status, a hub that never answers, no port written — leaves the
// command to the direct path.
func TestHubFlowAnswersOnlyAVerdictItRecognises(t *testing.T) {
	hubHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hubHome, "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	getenv := func(key string) (string, bool) {
		switch key {
		case "HUDDLE_HOME":
			return hubHome, true
		case "HUDDLE_TOKEN":
			return "test-token", true
		}
		return "", false
	}
	req := board.FlowRequest{Command: "pull", Actor: "w1", ID: "FLW-1"}
	const key = "abc123def456"

	// fakeHub starts one hub answering the two paths the flow client takes.
	fakeHub := func(t *testing.T, serve func(w http.ResponseWriter, r *http.Request)) string {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(serve))
		t.Cleanup(srv.Close)
		parsed, err := url.Parse(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.Port()
	}
	writePort := func(t *testing.T, port string) {
		t.Helper()
		if err := os.WriteFile(home.Home{Root: hubHome}.PortFile(), []byte(port), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("200 answers the lines", func(t *testing.T) {
		port := fakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			var got board.FlowRequest
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || !reflect.DeepEqual(got, req) {
				t.Errorf("the flow body = %v (%v), want the command's request", got, err)
			}
			if got := r.Header.Get("x-huddle-token"); got != "test-token" {
				t.Errorf("the token header = %q, want the test token", got)
			}
			if got := r.Header.Get("x-huddle-action"); got != r.URL.Path {
				t.Errorf("the action header = %q, want the route's path", got)
			}
			_, _ = w.Write([]byte(`{"lines":["one","two"]}`))
		})
		writePort(t, port)
		lines, refusal, handled := hubFlow(context.Background(), getenv, key, req)
		if !handled || refusal != "" {
			t.Fatalf("the 200 = handled %t refusal %q, want handled with no refusal", handled, refusal)
		}
		if strings.Join(lines, "|") != "one|two" {
			t.Fatalf("the 200's lines = %v, want [one two]", lines)
		}
	})
	t.Run("409 answers the refusal", func(t *testing.T) {
		port := fakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"FLW-1 is held by w2"}`))
		})
		writePort(t, port)
		lines, refusal, handled := hubFlow(context.Background(), getenv, key, req)
		if !handled || lines != nil {
			t.Fatalf("the 409 = handled %t lines %v, want handled with no lines", handled, lines)
		}
		if refusal != "FLW-1 is held by w2" {
			t.Fatalf("the 409's refusal = %q, want the board's message", refusal)
		}
	})
	t.Run("503 from a satellite whose hub is away answers the refusal", func(t *testing.T) {
		port := fakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"the hub at https://hub.example is unreachable"}`))
		})
		writePort(t, port)
		lines, refusal, handled := hubFlow(context.Background(), getenv, key, req)
		if !handled || lines != nil || !strings.Contains(refusal, "unreachable") {
			t.Fatalf("the 503 = handled %t lines %v refusal %q, want a handled refusal", handled, lines, refusal)
		}
	})
	t.Run("a 200 that is no answer leaves the command", func(t *testing.T) {
		port := fakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			_, _ = w.Write([]byte("not json"))
		})
		writePort(t, port)
		if _, _, handled := hubFlow(context.Background(), getenv, key, req); handled {
			t.Fatal("a body that is no answer counts as handled")
		}
	})
	t.Run("a board the hub does not serve leaves the command", func(t *testing.T) {
		port := fakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"no board"}`))
		})
		writePort(t, port)
		if _, _, handled := hubFlow(context.Background(), getenv, key, req); handled {
			t.Fatal("a 404 counts as handled")
		}
	})
	t.Run("a hub that fails leaves the command", func(t *testing.T) {
		port := fakeHub(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
		})
		writePort(t, port)
		if _, _, handled := hubFlow(context.Background(), getenv, key, req); handled {
			t.Fatal("a 500 counts as handled")
		}
	})
	t.Run("a hub without its health path leaves the command", func(t *testing.T) {
		port := fakeHub(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		writePort(t, port)
		if _, _, handled := hubFlow(context.Background(), getenv, key, req); handled {
			t.Fatal("a hub that never answers health counts as handled")
		}
	})
	t.Run("no port written leaves the command", func(t *testing.T) {
		if err := os.Remove(home.Home{Root: hubHome}.PortFile()); err != nil {
			t.Fatal(err)
		}
		if _, _, handled := hubFlow(context.Background(), getenv, key, req); handled {
			t.Fatal("no port file counts as handled")
		}
	})
	t.Run("a port file that is no port leaves the command", func(t *testing.T) {
		writePort(t, "not a port")
		if _, _, handled := hubFlow(context.Background(), getenv, key, req); handled {
			t.Fatal("a port file that is no port counts as handled")
		}
	})
}

// TestFlowCommandsGoThroughTheHubThenDirectly runs the kanban word itself: with
// the hub up, a flow command from one clone of a repository lands on the board
// the hub serves and the audit names it; with the hub down, the same command
// works on the board the working directory resolves.
func TestFlowCommandsGoThroughTheHubThenDirectly(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	hubHome := t.TempDir()
	data := t.TempDir()
	const origin = "git@github.com:MuhmdRaouf/shared-board.git"
	const token = "test-root-token"
	hubRepo := repoWithOrigin(t, "hub", origin)
	workRepo := repoWithOrigin(t, "work", origin)
	hubRoot := filepath.Join(hubRepo, ".agents", "kanban")
	workRoot := filepath.Join(workRepo, ".agents", "kanban")
	for _, root := range []string{hubRoot, workRoot} {
		if _, err := board.Init(context.Background(), root, "", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	writeCard(t, hubRoot, "FLW-1")
	writeCard(t, workRoot, "WORK-1")

	// the hub server on its own port, on the home the flow client reads
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	if err := os.MkdirAll(filepath.Join(hubHome, "run"), 0o700); err != nil {
		t.Fatal(err)
	}

	returned := make(chan struct{})
	var log bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = homeserver.Serve(ctx, homeserver.Options{
			Host: "127.0.0.1", Port: port, PortNamed: true,
			Home: hubHome, Data: data, Token: token, AutoCreate: true,
			Stdout: &log, Stderr: &log,
		})
		close(returned)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-returned:
		case <-time.After(10 * time.Second):
			t.Error("the hub server outlived its context")
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealth(t, base)
	if err := os.WriteFile(home.Home{Root: hubHome}.PortFile(), []byte(strconv.Itoa(port)), 0o600); err != nil {
		t.Fatal(err)
	}

	// the registry knows one session working in the hub's repo: its board is the one the tab serves
	report := fmt.Sprintf(`{"session_id":"s-hub","cwd":%q,"last_event":"UserPromptSubmit","last_event_at":"2026-10-18T09:00:00.000Z"}`, hubRepo)
	if status, body := hubCall(t, base, token, http.MethodPost, "/api/registry/report", report); status != 200 {
		t.Fatalf("report the session = %d %s", status, body)
	}

	t.Setenv("HUDDLE_HOME", hubHome)
	t.Setenv("HUDDLE_TOKEN", token)
	t.Chdir(workRepo)

	// through the hub: the hub's card moves, the local one stays
	var out, errOut bytes.Buffer
	if code := Run([]string{"pull", "--actor", "w1", "--id", "FLW-1"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("pull through the hub = %d, out %q, err %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "in-progress") || !strings.Contains(out.String(), "FLW-1") {
		t.Errorf("pull through the hub printed %q, want the card's new column", out.String())
	}
	if _, err := os.Stat(filepath.Join(hubRoot, "in-progress", "FLW-1.md")); err != nil {
		t.Fatalf("the hub's board did not take the write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workRoot, "in-progress", "WORK-1.md")); err == nil {
		t.Error("the local board took a write the hub handled")
	}

	// the audit names the write: the command, the card and the board
	key := board.RepoKey(origin, kanbanserver.KeyOf(hubRoot))
	_, body := hubCall(t, base, token, http.MethodGet, "/api/audit", "")
	var audit struct {
		Entries []struct {
			Actor  string `json:"actor"`
			Action string `json:"action"`
			Target string `json:"target"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &audit); err != nil {
		t.Fatalf("decode the audit %s: %v", body, err)
	}
	found := false
	for _, row := range audit.Entries {
		if row.Action == "kanban.pull" && row.Target == key+"/FLW-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("the audit holds no kanban.pull row for %s/FLW-1: %s", key, body)
	}

	// the hub down: the same command works on the local board
	cancel()
	<-returned
	out.Reset()
	errOut.Reset()
	if code := Run([]string{"pull", "--actor", "w1", "--id", "WORK-1"}, &out, &errOut, "test"); code != 0 {
		t.Fatalf("pull with the hub down = %d, out %q, err %q", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(workRoot, "in-progress", "WORK-1.md")); err != nil {
		t.Fatalf("the local board did not take the write: %v", err)
	}
}

// repoWithOrigin answers one git repository carrying an origin remote and no
// commit, in the test's temp tree.
func repoWithOrigin(t *testing.T, name, origin string) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), name)
	if out, err := exec.Command("git", "init", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v\n%s", name, err, out)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
		t.Fatalf("git remote add %s: %v\n%s", name, err, out)
	}
	return repo
}

// writeCard puts one bare ticket into a board's ready column.
func writeCard(t *testing.T, root, id string) {
	t.Helper()
	card := "---\nid: " + id + "\ntitle: \"one card\"\ntype: docs\ncluster: kanban\n" +
		"summary: \"one\"\n---\n\n# " + id + "\n\nBody text.\n"
	if err := os.WriteFile(filepath.Join(root, "ready", id+".md"), []byte(card), 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitForHealth blocks until the base answers huddle's health path.
func waitForHealth(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the hub server never answered /health")
}

// hubCall runs one credentialed request against the hub; a write carries the
// route's path as its action, the way the real clients do.
func hubCall(t *testing.T, base, token, method, path, body string) (int, string) {
	t.Helper()
	var payload io.Reader
	if body != "" {
		payload = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, base+path, payload)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-huddle-token", token)
	if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
		req.Header.Set("x-huddle-action", path)
		req.Header.Set("content-type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}
