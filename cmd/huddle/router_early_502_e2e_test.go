package main

// The router's side of a 502 that lands before the answer's head, end to end: a fake upstream
// fails the first request with Anthropic's error shape and no stream byte, and the daemon hands
// that 502 to the client whole — the JSON error, no `event:` line — and streams the very next
// request through the same router to its end. Whether Claude Code retries such a 502 is outside
// what these suites can prove (the fake claude is huddle's own stand-in): docs/TESTPLAN.md holds
// that gap under "Needs a human". The daemon is this test's own child on a free port and a temp
// home.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/pki"
	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// early502Fake counts the requests it takes: the first answers 502 with Anthropic's error shape
// and Content-Type application/json before any stream byte; every later one streams the four
// SSE events of an answer (message_start, content_block_delta, message_delta, message_stop) and
// ends. The returned func reads the count.
func early502Fake(t *testing.T) (*httptest.Server, func() int64) {
	t.Helper()
	var seen atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if seen.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"upstream hiccup"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		write := func(event, data string) {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
			flusher.Flush()
		}
		write("message_start", `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"usage":{"input_tokens":10,"output_tokens":1}}}`)
		write("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`)
		write("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`)
		write("message_stop", `{"type":"message_stop"}`)
	}))
	t.Cleanup(server.Close)
	return server, seen.Load
}

// startRouterAgainst starts the real router daemon against upstreamURL on a free port and a
// temp home — the overhead test's recipe: providers.json with zai, `huddle router start`, the
// health poll, the pid from run/router.json — and registers the SIGTERM stop. It answers the
// router's own URL.
func startRouterAgainst(t *testing.T, upstreamURL string) string {
	t.Helper()
	root := t.TempDir()
	routerHome := filepath.Join(root, "router-home")
	routerPort := ports.Free(t)
	if err := os.MkdirAll(routerHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(routerHome, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	env := isoenv.Environ(t, filepath.Join(root, "user"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home"),
		"RADAR_HOME="+filepath.Join(root, "radar"),
		"HUDDLE_NO_PROJECT_CRED=1",
		pki.StoreOffEnv+"=1",
		"HUDDLE_ROUTER_HOME="+routerHome,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+upstreamURL,
		"ZAI_ROUTER_URL="+upstreamURL,
		"ZAI_API_KEY=test-key",
	)
	routerURL := fmt.Sprintf("http://127.0.0.1:%d", routerPort)
	healthy := func() bool {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(routerURL + "/huddle-router/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}
	start := exec.Command(huddleBin, "router", "start")
	start.Env = env
	start.Dir = root
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("router start: %v\n%s", err, out)
	}
	if !pollFor(5*time.Second, healthy) {
		t.Fatal("the daemon never answered its health path")
	}
	daemonPID := 0
	if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
		var record struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(raw, &record) == nil {
			daemonPID = record.PID
		}
	}
	if daemonPID <= 0 {
		t.Fatal("the daemon published no pid")
	}
	t.Cleanup(func() {
		_ = syscall.Kill(daemonPID, syscall.SIGTERM)
		if !pollFor(5*time.Second, func() bool { return !healthy() }) {
			_ = syscall.Kill(daemonPID, syscall.SIGKILL)
			pollFor(2*time.Second, func() bool { return !healthy() })
		}
	})
	return routerURL
}

// TestE2ERouterEarly502IsWholeAndTheNextRequestFinishes posts two streaming turns for an
// Anthropic model through the one daemon: the first, whose upstream fails before its answer's
// head, reaches the client as the whole 502; the second streams to message_stop.
func TestE2ERouterEarly502IsWholeAndTheNextRequestFinishes(t *testing.T) {
	fake, seen := early502Fake(t)
	routerURL := startRouterAgainst(t, fake.URL)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DisableCompression: true}}
	post := func() (*http.Response, []byte, error) {
		body := `{"model":"claude-opus-5-5","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
		resp, err := client.Post(routerURL+"/v1/messages", "application/json", strings.NewReader(body))
		if err != nil {
			return nil, nil, err
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, raw, err
	}

	// The first turn: the upstream fails before its answer's head, and the 502 crosses whole.
	resp, body, err := post()
	if err != nil {
		t.Fatalf("the first request: %v", err)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("the first answer = %d, want 502 (body %s)", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "json") {
		t.Errorf("the first answer's Content-Type = %q, want JSON", ct)
	}
	var shape struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &shape); err != nil {
		t.Errorf("the 502 body did not parse as JSON: %v\n%s", err, body)
	} else {
		if shape.Type != "error" {
			t.Errorf("the 502 body's type = %q, want error (body %s)", shape.Type, body)
		}
		if shape.Error.Type == "" {
			t.Errorf("the 502 body's inner error type is empty (body %s)", body)
		}
	}
	if strings.Contains(string(body), "event:") {
		t.Errorf("the 502 body carries a stream line (body %s)", body)
	}

	// The very next turn through the same router streams to its end.
	resp, body, err = post()
	if err != nil {
		t.Fatalf("the second request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the second answer = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "message_stop") {
		t.Errorf("the second answer never reached message_stop (body %s)", body)
	}

	if got := seen(); got != 2 {
		t.Errorf("the fake answered %d requests, want 2", got)
	}
}
