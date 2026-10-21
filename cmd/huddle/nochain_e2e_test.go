package main

// The 2026-10-21 incident, end to end on the built binary: a huddle-router whose proxy
// variables name a second huddle-router goes straight to the upstream instead of chaining.
// Router A serves one port, router B another; B's HTTPS_PROXY and HTTP_PROXY name A; a claude
// request to B is answered from B's own Anthropic while A never hears of it. Every process
// here is this test's child, every home a temp one.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MuhmdRaouf/huddle/internal/testkit/isoenv"
	"github.com/MuhmdRaouf/huddle/internal/testkit/ports"
)

// upstreamFake is one recording Anthropic the routers are pointed at: it answers every
// /v1/messages with a JSON body.
type upstreamFake struct {
	server *httptest.Server
	mu     sync.Mutex
	paths  []string
}

func newUpstreamFake(t *testing.T) *upstreamFake {
	fake := &upstreamFake{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.paths = append(fake.paths, r.URL.Path)
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"msg_fake","type":"message","role":"assistant","content":[]}`)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *upstreamFake) messages() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, path := range f.paths {
		if path == "/v1/messages" {
			count++
		}
	}
	return count
}

// routerHomeFiles lays out one router home with zai enabled, so the daemon serves the port.
func routerHomeFiles(t *testing.T, home string) {
	t.Helper()
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "providers.json"), []byte(`{"providers":["zai"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// postClaude posts one claude request to a router port and answers the status and body.
func postClaude(t *testing.T, port int) (int, string) {
	t.Helper()
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post("http://127.0.0.1:"+strconv.Itoa(port)+"/v1/messages", "application/json",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
	if err != nil {
		t.Fatalf("post claude-sonnet-4-5: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	return resp.StatusCode, string(body)
}

func TestE2ENoChainARouterNeverRoutesThroughAnotherRouter(t *testing.T) {
	root := t.TempDir()
	routerHomeA := filepath.Join(root, "router-home-a")
	routerHomeB := filepath.Join(root, "router-home-b")
	routerHomeFiles(t, routerHomeA)
	routerHomeFiles(t, routerHomeB)
	anthropicA := newUpstreamFake(t)
	anthropicB := newUpstreamFake(t)
	zai := newUpstreamFake(t)
	portA, portB := ports.Free(t), ports.Free(t)
	envA := isoenv.Lookup(isoenv.Environ(t, filepath.Join(root, "user-a"),
		"RADAR_HOME="+filepath.Join(root, "radar-a"),
		"HUDDLE_HOME="+filepath.Join(root, "huddle-home-a"),
		"HUDDLE_NO_PROJECT_CRED=1",
		"HUDDLE_ROUTER_HOME="+routerHomeA,
		"HUDDLE_ROUTER_PORT="+strconv.Itoa(portA),
		"HUDDLE_ROUTER_ANTHROPIC_URL="+anthropicA.server.URL,
		"ZAI_ROUTER_URL="+zai.server.URL,
		"ZAI_API_KEY=nochain-e2e-key",
	))
	envB := argvLayer(envA, map[string]string{
		"HOME":                        filepath.Join(root, "user-b"),
		"RADAR_HOME":                  filepath.Join(root, "radar-b"),
		"HUDDLE_HOME":                 filepath.Join(root, "huddle-home-b"),
		"HUDDLE_ROUTER_HOME":          routerHomeB,
		"HUDDLE_ROUTER_PORT":          strconv.Itoa(portB),
		"HUDDLE_ROUTER_ANTHROPIC_URL": anthropicB.server.URL,
		"HTTPS_PROXY":                 "http://127.0.0.1:" + strconv.Itoa(portA),
		"HTTP_PROXY":                  "http://127.0.0.1:" + strconv.Itoa(portA),
	})
	pids := newPidWatch(t)

	t.Run("router A comes up on its port", func(t *testing.T) {
		if code, out, errOut := runBin(t, huddleBin, envA, root, "router", "start"); code != 0 {
			t.Fatalf("router A start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		until(t, 15*time.Second, func() bool { return routerHealthy(portA) }, "router A to answer its health path")
		pids.see(awaitHandoverRecord(t, routerHomeA).PID)
	})

	t.Run("router B, whose proxy names router A, comes up on its own port", func(t *testing.T) {
		if code, out, errOut := runBin(t, huddleBin, envB, root, "router", "start"); code != 0 {
			t.Fatalf("router B start = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
		}
		until(t, 15*time.Second, func() bool { return routerHealthy(portB) }, "router B to answer its health path")
		pids.see(awaitHandoverRecord(t, routerHomeB).PID)
	})

	t.Run("a claude request to B is answered by B's own Anthropic", func(t *testing.T) {
		status, body := postClaude(t, portB)
		if status != 200 {
			t.Fatalf("claude-sonnet-4-5 through B = %d %q", status, body)
		}
		if got := anthropicB.messages(); got != 1 {
			t.Fatalf("B's Anthropic saw %d /v1/messages requests, want 1", got)
		}
	})

	t.Run("router A carried nothing", func(t *testing.T) {
		if got := anthropicA.messages(); got != 0 {
			t.Fatalf("A's Anthropic saw %d /v1/messages requests, want 0", got)
		}
		raw, err := os.ReadFile(filepath.Join(routerHomeA, "router.log"))
		if err != nil {
			t.Fatal(err)
		}
		logA := string(raw)
		for _, banned := range []string{"CONNECT", "/v1/messages", "claude-sonnet-4-5"} {
			if strings.Contains(logA, banned) {
				t.Errorf("A's log carries %q:\n%s", banned, logA)
			}
		}
	})

	t.Run("B's log says it recognised the proxy as a router and went direct", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(routerHomeB, "router.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "is a router; going direct") {
			t.Errorf("B's log =\n%s\nwant the going-direct line", raw)
		}
	})
}
