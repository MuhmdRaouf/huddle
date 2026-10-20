package main

// The huddle-router words, end to end: `router start` brings one daemon up for every enabled
// provider, a glm request and a claude request reach their own upstreams through it, a second
// serve steps aside, and status names what the router serves. Every process here is this test's
// child, every home a temp one, and the daemon dies with the test.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// routerFake is one fake upstream the e2e points the router at: it records what it got and
// answers a two-event stream named after itself.
type routerFake struct {
	server *httptest.Server
	mu     sync.Mutex
	seen   []routerSeen
}

type routerSeen struct {
	path string
	auth string
	body string
}

func newRouterFake(t *testing.T, name string) *routerFake {
	t.Helper()
	fake := &routerFake{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.seen = append(fake.seen, routerSeen{path: r.URL.Path, auth: r.Header.Get("Authorization"), body: string(body)})
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+name+"-1\n\n")
		_, _ = io.WriteString(w, "data: "+name+"-2\n\n")
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *routerFake) requests() []routerSeen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]routerSeen(nil), f.seen...)
}

func TestE2EHuddleRouterWords(t *testing.T) {
	root := t.TempDir()
	routerHome := filepath.Join(root, "router-home")
	anthropic := newRouterFake(t, "anthropic")
	zai := newRouterFake(t, "zai")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	routerPort := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	env := argvLayer(map[string]string{
		"PATH":                        os.Getenv("PATH"),
		"HOME":                        filepath.Join(root, "user"),
		"XDG_CONFIG_HOME":             filepath.Join(root, "xdg-config"),
		"XDG_STATE_HOME":              filepath.Join(root, "xdg-state"),
		"RADAR_HOME":                  filepath.Join(root, "radar"),
		"HUDDLE_HOME":                 filepath.Join(root, "huddle-home"),
		"HUDDLE_NO_PROJECT_CRED":      "1",
		"HUDDLE_ROUTER_HOME":          routerHome,
		"HUDDLE_ROUTER_PORT":          strconv.Itoa(routerPort),
		"HUDDLE_ROUTER_ANTHROPIC_URL": anthropic.server.URL,
		"ZAI_ROUTER_URL":              zai.server.URL,
		"ZAI_API_KEY":                 "test-key",
	}, nil)
	providersFile := filepath.Join(routerHome, "providers.json")
	enable := func(names ...string) {
		t.Helper()
		if err := os.MkdirAll(routerHome, 0o700); err != nil {
			t.Fatal(err)
		}
		quoted := make([]string, 0, len(names))
		for _, name := range names {
			quoted = append(quoted, strconv.Quote(name))
		}
		text := `{"providers":[` + strings.Join(quoted, ",") + `]}`
		if err := os.WriteFile(providersFile, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	healthy := func() bool {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/huddle-router/health", routerPort))
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	}

	t.Run("start with no provider enabled says so and spawns nothing", func(t *testing.T) {
		code, out, errOut := argvRun(t, env, root, "router", "start")
		if code != 0 {
			t.Fatalf("router start = %d, stdout %q, stderr %q", code, out, errOut)
		}
		if !strings.Contains(out, "huddle-router: no provider is set up") {
			t.Errorf("stdout = %q, want the no-provider line", out)
		}
		if healthy() {
			t.Error("a daemon came up with no provider enabled")
		}
	})

	t.Run("start after enabling zai brings the daemon up and prints its address", func(t *testing.T) {
		enable("zai")
		code, out, errOut := argvRun(t, env, root, "router", "start")
		if code != 0 {
			t.Fatalf("router start = %d, stdout %q, stderr %q", code, out, errOut)
		}
		want := fmt.Sprintf("huddle-router: http://127.0.0.1:%d (zai)\n", routerPort)
		if out != want {
			t.Errorf("stdout = %q, want %q", out, want)
		}
		if !pollFor(5*time.Second, healthy) {
			t.Fatal("the daemon never answered its health path")
		}
	})

	// From here on a daemon runs: this test's own child, ended in the cleanup below.
	daemonPID := 0
	if raw, err := os.ReadFile(filepath.Join(routerHome, "run", "router.json")); err == nil {
		var record struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(raw, &record) == nil {
			daemonPID = record.PID
		}
	}
	t.Cleanup(func() {
		if daemonPID <= 0 {
			return
		}
		_ = syscall.Kill(daemonPID, syscall.SIGTERM)
		if !pollFor(5*time.Second, func() bool { return !healthy() }) {
			_ = syscall.Kill(daemonPID, syscall.SIGKILL)
			pollFor(2*time.Second, func() bool { return !healthy() })
		}
	})

	t.Run("a glm request reaches zai with its key and a claude request reaches Anthropic", func(t *testing.T) {
		client := &http.Client{Timeout: 10 * time.Second}
		post := func(model string) (int, string) {
			resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/v1/messages", routerPort), "application/json",
				strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[]}`, model)))
			if err != nil {
				t.Fatalf("post %s: %v", model, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read the answer: %v", err)
			}
			return resp.StatusCode, string(body)
		}
		if status, body := post("glm-5.3"); status != 200 {
			t.Fatalf("glm-5.3 = %d %q", status, body)
		}
		if status, body := post("claude-opus-5-5"); status != 200 {
			t.Fatalf("claude-opus-5-5 = %d %q", status, body)
		}
		zaiSeen := zai.requests()
		if len(zaiSeen) != 1 {
			t.Fatalf("zai saw %d requests, want 1", len(zaiSeen))
		}
		if zaiSeen[0].auth != "Bearer test-key" {
			t.Errorf("zai's authorization = %q, want the env key", zaiSeen[0].auth)
		}
		anthropicSeen := anthropic.requests()
		if len(anthropicSeen) != 1 {
			t.Fatalf("Anthropic saw %d requests, want 1", len(anthropicSeen))
		}
	})

	t.Run("a second serve exits 0 saying it is already served", func(t *testing.T) {
		code, out, errOut := argvRun(t, env, root, "router", "serve")
		if code != 0 {
			t.Fatalf("router serve = %d, stdout %q, stderr %q", code, out, errOut)
		}
		if !strings.Contains(out, "huddle-router: already served by pid") {
			t.Errorf("stdout = %q, want the already-served line", out)
		}
		if !healthy() {
			t.Error("the first daemon stopped serving")
		}
	})

	t.Run("status --json names zai", func(t *testing.T) {
		code, out, errOut := argvRun(t, env, root, "router", "status", "--json")
		if code != 0 {
			t.Fatalf("router status = %d, stdout %q, stderr %q", code, out, errOut)
		}
		var answer struct {
			Running   bool     `json:"running"`
			Name      string   `json:"name"`
			Providers []string `json:"providers"`
		}
		if err := json.Unmarshal([]byte(out), &answer); err != nil {
			t.Fatalf("status --json = %q: %v", out, err)
		}
		if !answer.Running || answer.Name != "huddle-router" {
			t.Errorf("status = %+v, want huddle-router running", answer)
		}
		if len(answer.Providers) != 1 || answer.Providers[0] != "zai" {
			t.Errorf("status providers = %v, want [zai]", answer.Providers)
		}
	})
}
